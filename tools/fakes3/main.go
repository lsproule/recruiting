// Command fakes3 is an in-memory stand-in for the S3-compatible object store
// the platform expects, for development hosts that cannot run MinIO. It
// speaks just enough of the S3 wire protocol for internal/blob — bucket
// existence and creation, put, head, get, delete, and presigned downloads —
// and checks no signature at all. Never expose it beyond localhost.
//
//	go run ./tools/fakes3 -listen :9000
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/md5" //nolint:gosec // S3 ETags are MD5 by definition; nothing here is a secret.
)

type object struct {
	body        []byte
	contentType string
	modified    time.Time
	etag        string
}

type store struct {
	mu      sync.RWMutex
	buckets map[string]map[string]*object
}

func newStore() *store { return &store{buckets: map[string]map[string]*object{}} }

func (s *store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket == "" {
		// The root is the liveness probe: it lists the buckets, which is
		// what S3 does there too.
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Buckets></Buckets></ListAllMyBucketsResult>`)
		return
	}
	if key == "" {
		s.bucketOp(w, r, bucket)
		return
	}
	s.objectOp(w, r, bucket, key)
}

func (s *store) bucketOp(w http.ResponseWriter, r *http.Request, bucket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.buckets[bucket]
	switch r.Method {
	case http.MethodHead:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		if !exists {
			s.buckets[bucket] = map[string]*object{}
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if !exists {
			s3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
			return
		}
		if r.URL.Query().Has("location") {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`)
			return
		}
		// A listing: only what the tests might ask for, an empty page.
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>`+bucket+`</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *store) objectOp(w http.ResponseWriter, r *http.Request, bucket, key string) {
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// aws-chunked uploads carry chunk framing; strip it when present.
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING") {
			body = dechunk(body)
		}
		sum := md5.Sum(body) //nolint:gosec
		obj := &object{body: body, contentType: r.Header.Get("Content-Type"), modified: time.Now().UTC(), etag: `"` + hex.EncodeToString(sum[:]) + `"`}
		s.mu.Lock()
		if _, ok := s.buckets[bucket]; !ok {
			s.mu.Unlock()
			s3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
			return
		}
		s.buckets[bucket][key] = obj
		s.mu.Unlock()
		w.Header().Set("ETag", obj.etag)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead, http.MethodGet:
		s.mu.RLock()
		obj := s.buckets[bucket][key]
		s.mu.RUnlock()
		if obj == nil {
			s3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist")
			return
		}
		w.Header().Set("Content-Type", obj.contentType)
		w.Header().Set("ETag", obj.etag)
		w.Header().Set("Last-Modified", obj.modified.Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		if cd := r.URL.Query().Get("response-content-disposition"); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		http.ServeContent(w, r, key, obj.modified, bytes.NewReader(obj.body))
	case http.MethodDelete:
		s.mu.Lock()
		if b, ok := s.buckets[bucket]; ok {
			delete(b, key)
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// dechunk unframes an aws-chunked body: hex size, ";chunk-signature=…",
// CRLF, data, CRLF, until a zero-size chunk.
func dechunk(body []byte) []byte {
	var out []byte
	rest := body
	for len(rest) > 0 {
		line, after, ok := bytes.Cut(rest, []byte("\r\n"))
		if !ok {
			break
		}
		sizeHex, _, _ := strings.Cut(string(line), ";")
		size, err := strconv.ParseInt(strings.TrimSpace(sizeHex), 16, 64)
		if err != nil || size == 0 || int64(len(after)) < size {
			break
		}
		out = append(out, after[:size]...)
		rest = after[size:]
		rest = bytes.TrimPrefix(rest, []byte("\r\n"))
	}
	return out
}

func s3Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, message)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:9000", "address to listen on")
	flag.Parse()
	log.Printf("fakes3: in-memory object store on %s (development only; no authentication)", *listen)
	srv := &http.Server{Addr: *listen, Handler: newStore(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
