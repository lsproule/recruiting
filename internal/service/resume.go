package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// What became of a resume's text; mirrors the resume.text_status check.
const (
	ResumePending   = "pending"
	ResumeExtracted = "extracted"
	ResumeFailed    = "failed"
)

// ResumeURLTTL is how long a resume download link stays valid. Short enough
// that a copied link is not a lasting handle on the file.
const ResumeURLTTL = 10 * time.Minute

var ErrNoBlobStore = errors.New("service: object storage is not configured")

// BlobStore is the slice of object storage a resume needs; blob.Client
// satisfies it.
type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	SignedGetURL(ctx context.Context, key, filename string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
}

// Resume is one uploaded CV. The bytes stay in object storage; only the
// metadata and the extracted text live in Postgres.
type Resume struct {
	ID          uuid.UUID
	CandidateID uuid.UUID
	Filename    string
	ContentType string
	SizeBytes   int64
	TextStatus  string
	CreatedAt   time.Time
}

// ResumeUpload is a file as it arrived from a form.
type ResumeUpload struct {
	Filename string
	Data     []byte
}

// ResumeService stores resumes and hands out download links.
type ResumeService struct {
	st   *store.Store
	blob BlobStore
	// Logger records an orphaned object that could not be cleaned up. Nil
	// disables that logging.
	Logger *slog.Logger
}

func NewResumeService(st *store.Store, b BlobStore) *ResumeService {
	return &ResumeService{st: st, blob: b}
}

// storedResume is an upload that is already in object storage and read, but
// not yet in any row. Splitting it this way keeps the slow work — the PUT and
// the extraction — outside the transaction that writes the application.
type storedResume struct {
	key         string
	filename    string
	contentType string
	size        int64
	text        *string
	status      string
}

// prepare validates the upload by its bytes, writes it to object storage, and
// extracts its text. Extraction is bounded and never fatal: a resume whose
// text cannot be read is stored unindexed. The caller must either insert the
// result in a transaction or discard it.
func (s *ResumeService) prepare(ctx context.Context, orgID uuid.UUID, up ResumeUpload) (storedResume, error) {
	contentType, err := ValidateResume(up)
	if err != nil {
		return storedResume{}, err
	}
	if s.blob == nil {
		return storedResume{}, ErrNoBlobStore
	}
	out := storedResume{
		key:         resumeKey(orgID, contentType),
		filename:    cleanFilename(up.Filename, contentType),
		contentType: contentType,
		size:        int64(len(up.Data)),
		status:      ResumeExtracted,
	}
	if err := s.blob.Put(ctx, out.key, bytes.NewReader(up.Data), out.size, contentType); err != nil {
		return storedResume{}, fmt.Errorf("store resume: %w", err)
	}

	extractCtx, cancel := context.WithTimeout(ctx, domain.ExtractTimeout)
	defer cancel()
	body, err := domain.ExtractText(extractCtx, contentType, up.Data)
	// The column is indexed into a tsvector, which has its own ceiling; the
	// text is capped again here so no extractor can exceed it.
	if body = domain.TruncateText(body); err != nil || strings.TrimSpace(body) == "" {
		out.status = ResumeFailed
	} else {
		out.text = &body
	}
	return out, nil
}

// insert records a prepared resume against a candidate inside the caller's
// transaction.
func insertResume(ctx context.Context, tx *store.Tx, orgID, candidateID uuid.UUID, r storedResume) (Resume, error) {
	row, err := tx.Q.CreateResume(ctx, db.CreateResumeParams{
		OrgID: orgID, CandidateID: candidateID, BlobKey: r.key,
		Filename: r.filename, ContentType: r.contentType, SizeBytes: r.size,
		ExtractedText: r.text, TextStatus: r.status,
	})
	if err != nil {
		return Resume{}, err
	}
	return toResume(row), nil
}

// discard removes an object whose transaction never committed, so a rolled
// back application leaves nothing behind in the bucket.
func (s *ResumeService) discard(ctx context.Context, key string) {
	if key == "" || s.blob == nil {
		return
	}
	if err := s.blob.Delete(ctx, key); err != nil && s.Logger != nil {
		s.Logger.ErrorContext(ctx, "orphaned resume object", "key", key, "error", err)
	}
}

// DownloadURL signs a short-lived link to one of the org's resumes. Both ids
// are matched, so a resume id cannot be read through another candidate's URL.
func (s *ResumeService) DownloadURL(ctx context.Context, p Principal, candidateID, resumeID uuid.UUID) (string, error) {
	if p.Kind != PrincipalOrgUser {
		return "", ErrForbidden
	}
	if s.blob == nil {
		return "", ErrNoBlobStore
	}
	var row db.Resume
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		row, err = tx.Q.GetCandidateResume(ctx, db.GetCandidateResumeParams{ID: resumeID, CandidateID: candidateID})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resume url: %w", err)
	}
	url, err := s.blob.SignedGetURL(ctx, row.BlobKey, row.Filename, ResumeURLTTL)
	if err != nil {
		return "", fmt.Errorf("resume url: %w", err)
	}
	return url, nil
}

// ValidateResume decides what an upload is from its bytes alone; the
// filename an uploader chose proves nothing.
func ValidateResume(up ResumeUpload) (string, error) {
	if len(up.Data) == 0 {
		return "", domain.ErrResumeEmpty
	}
	if len(up.Data) > domain.MaxResumeBytes {
		return "", domain.ErrResumeTooLarge
	}
	return domain.SniffResume(up.Data)
}

func resumeExtension(contentType string) string {
	if contentType == domain.ResumeDOCX {
		return ".docx"
	}
	return ".pdf"
}

// resumeKey namespaces objects by org so a bucket listing is readable and one
// org's prefix never overlaps another's. The candidate is not in the key: the
// object is written before the candidate row exists.
func resumeKey(orgID uuid.UUID, contentType string) string {
	return "resumes/" + orgID.String() + "/" + uuid.NewString() + resumeExtension(contentType)
}

// cleanFilename keeps the uploader's name for the download prompt but strips
// any directory part, and forces the extension to match the sniffed type.
func cleanFilename(name, contentType string) string {
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, `\`, "/")))
	ext := resumeExtension(contentType)
	base := strings.TrimSuffix(name, path.Ext(name))
	if base == "" || base == "." || base == ".." {
		base = "resume"
	}
	return base + ext
}

func toResume(row db.Resume) Resume {
	return Resume{
		ID: row.ID, CandidateID: row.CandidateID, Filename: row.Filename,
		ContentType: row.ContentType, SizeBytes: row.SizeBytes,
		TextStatus: row.TextStatus, CreatedAt: row.CreatedAt.Time,
	}
}
