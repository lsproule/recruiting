// Command fakes3 is an in-memory stand-in for the S3-compatible object store
// the platform expects, for development hosts that cannot run MinIO. It
// speaks just enough of the S3 wire protocol for internal/blob — bucket
// existence and creation, put, head, get, delete, and presigned downloads —
// and checks no signature at all. Never expose it beyond localhost.
//
// Everything lives in memory, so it is bounded: one object may be at most
// -max-object-mb, and when the store holds more than -max-mb in all, the
// objects written longest ago are dropped to make room.
//
//	go run ./tools/fakes3 -listen :9000
package main

import (
	"bytes"
	"container/list"
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

// Defaults for the memory bounds, in MiB.
const (
	defaultMaxMB       = 512
	defaultMaxObjectMB = 64
)

type object struct {
	bucket, key string
	body        []byte
	contentType string
	modified    time.Time
	etag        string
	// elem is the object's place in the store's age order.
	elem *list.Element
}

type store struct {
	mu      sync.RWMutex
	buckets map[string]map[string]*object
	// order holds every object oldest write first; eviction takes from the
	// front. resident is the sum of their bodies.
	order       *list.List
	resident    int64
	maxResident int64
	maxObject   int64
}

// newStore bounds the store at maxResident bytes in all and maxObject bytes
// per object; an object that could never fit is refused rather than stored.
func newStore(maxResident, maxObject int64) *store {
	if maxObject > maxResident {
		maxObject = maxResident
	}
	return &store{buckets: map[string]map[string]*object{}, order: list.New(), maxResident: maxResident, maxObject: maxObject}
}

// hasBucket reports whether bucket exists, without holding the lock for
// anything else.
func (s *store) hasBucket(bucket string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.buckets[bucket]
	return ok
}

// unlink takes obj out of the bucket and the age order and returns its
// bytes to the budget. It runs under the write lock.
func (s *store) unlink(obj *object) {
	if b, ok := s.buckets[obj.bucket]; ok && b[obj.key] == obj {
		delete(b, obj.key)
	}
	if obj.elem != nil {
		s.order.Remove(obj.elem)
		obj.elem = nil
	}
	s.resident -= int64(len(obj.body))
	obj.body = nil
}

// insert stores obj, replacing what its key held and evicting the oldest
// objects until it fits. It runs under the write lock and reports false when
// the bucket is gone.
func (s *store) insert(obj *object) bool {
	b, ok := s.buckets[obj.bucket]
	if !ok {
		return false
	}
	if old := b[obj.key]; old != nil {
		s.unlink(old)
	}
	for s.resident+int64(len(obj.body)) > s.maxResident && s.order.Len() > 0 {
		oldest, _ := s.order.Front().Value.(*object)
		log.Printf("fakes3: evicting %s/%s (%d bytes) to stay under %d bytes", oldest.bucket, oldest.key, len(oldest.body), s.maxResident)
		s.unlink(oldest)
	}
	b[obj.key] = obj
	obj.elem = s.order.PushBack(obj)
	s.resident += int64(len(obj.body))
	return true
}

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
		s.putObject(w, r, bucket, key)
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
		if obj := s.buckets[bucket][key]; obj != nil {
			s.unlink(obj)
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// putObject stores one upload. The bucket is checked before a byte of the
// body is read, and the body is read through a cap, so a client cannot make
// the process hold what it will not keep.
func (s *store) putObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if !s.hasBucket(bucket) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	chunked := strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING")
	// A truthful length is refused without reading anything. An aws-chunked
	// body declares its payload length apart from its framing.
	declared := r.ContentLength
	if chunked {
		if n, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64); err == nil {
			declared = n
		}
	}
	if declared > s.maxObject {
		s3Error(w, http.StatusRequestEntityTooLarge, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size")
		return
	}
	// The read cap leaves room for chunk framing: a header line of under a
	// hundred bytes per 64 KiB chunk, and a trailer.
	limit := s.maxObject
	if chunked {
		limit += limit/256 + 64<<10
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if int64(len(body)) > limit {
		s3Error(w, http.StatusRequestEntityTooLarge, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size")
		return
	}
	if chunked {
		body = dechunk(body)
	}
	if int64(len(body)) > s.maxObject {
		s3Error(w, http.StatusRequestEntityTooLarge, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size")
		return
	}
	sum := md5.Sum(body) //nolint:gosec
	obj := &object{bucket: bucket, key: key, body: body, contentType: r.Header.Get("Content-Type"), modified: time.Now().UTC(), etag: `"` + hex.EncodeToString(sum[:]) + `"`}
	s.mu.Lock()
	ok := s.insert(obj)
	s.mu.Unlock()
	if !ok {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	w.Header().Set("ETag", obj.etag)
	w.WriteHeader(http.StatusOK)
}

// dechunk unframes an aws-chunked body in place: hex size,
// ";chunk-signature=…", CRLF, data, CRLF, until a zero-size chunk. The data
// is copied down over the framing it follows, so no second buffer is held.
func dechunk(body []byte) []byte {
	out := body[:0]
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
	maxMB := flag.Int64("max-mb", defaultMaxMB, "most MiB held in all; the oldest objects go first past it")
	maxObjectMB := flag.Int64("max-object-mb", defaultMaxObjectMB, "largest object accepted, in MiB")
	flag.Parse()
	log.Printf("fakes3: in-memory object store on %s (development only; no authentication; %d MiB in all, %d MiB per object)", *listen, *maxMB, *maxObjectMB)
	srv := &http.Server{Addr: *listen, Handler: newStore(*maxMB<<20, *maxObjectMB<<20), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
