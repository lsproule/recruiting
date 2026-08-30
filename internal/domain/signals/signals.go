package signals

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Signal names, in the order the signals are returned.
const (
	PasteRatio          = "paste_ratio"
	PasteThenPass       = "paste_then_pass"
	BurstTyping         = "burst_typing"
	EditRatio           = "edit_ratio"
	BlurThenSolution    = "blur_then_solution"
	SpeedVsDifficulty   = "speed_vs_difficulty"
	ReferenceSimilarity = "reference_similarity"
)

// Confidence levels; they mirror the integrity_signal.confidence check.
const (
	ConfidenceNormal = "normal"
	ConfidenceLow    = "low"
)

// Thresholds. Calibration against recorded sessions moves them here, in one
// place.
const (
	// PasteThenPassWindow is how soon after an external paste a passing
	// submit has to land to count.
	PasteThenPassWindow = 60 * time.Second
	// BlurMinDuration is how long the page must be out of focus before the
	// return is watched for a solution.
	BlurMinDuration = 30 * time.Second
	// BlurSolutionWindow is how long after the return that watch lasts.
	BlurSolutionWindow = 2 * time.Minute
	// BlurSolutionShare is the share of the final source that has to appear
	// in that window.
	BlurSolutionShare = 0.4
	// BurstRate is the typing rate, in characters per second, above which a
	// run of edits is a burst.
	BurstRate = 8.0
	// BurstMinDuration is how long the rate has to be sustained.
	BurstMinDuration = 5 * time.Second
	// BurstGap is the pause between two edits that ends a run.
	BurstGap = 2 * time.Second
	// LowEditRatio is the deletions-per-insertion ratio below which typing
	// starts looking transcribed rather than composed; the signal rises
	// linearly from there to 1 at no deletions at all.
	LowEditRatio = 0.15
	// MinTypedForEditRatio is how many characters must have been typed
	// before the edit ratio says anything.
	MinTypedForEditRatio = 50
)

// BandMedian is the reference time to a first pass for each difficulty;
// unknown difficulties are read as medium.
var BandMedian = map[string]time.Duration{
	"easy":   10 * time.Minute,
	"medium": 25 * time.Minute,
	"hard":   45 * time.Minute,
}

// Submission is one run or submit as the signals see it.
type Submission struct {
	ID        uuid.UUID
	ProblemID uuid.UUID
	Kind      string // run or submit
	At        time.Time
	// Passed is whether every case passed.
	Passed bool
	Source string
}

// Problem is what the signals know about a problem of the attempt.
type Problem struct {
	ID         uuid.UUID
	Difficulty string
	// FinalSource is the candidate's last text for the problem: the final
	// submit, or the last synced editor state. Empty falls back to the
	// source replayed from the stream.
	FinalSource string
	// References are the problem's reference solutions.
	References []string
	// Others are other candidates' submits of the problem within the org.
	Others []string
}

// Input is everything one attempt's signals are computed from.
type Input struct {
	StartedAt   time.Time
	Events      []Event
	Submissions []Submission
	Problems    []Problem
	// Incomplete is set when the recording has a gap; every signal is then
	// reported at low confidence but still computed.
	Incomplete bool
}

// Evidence is one observation behind a signal's value, for the reviewer.
type Evidence struct {
	ProblemID uuid.UUID          `json:"problem_id"`
	At        time.Time          `json:"at,omitempty"`
	Seq       int64              `json:"seq,omitempty"`
	Note      string             `json:"note"`
	Values    map[string]float64 `json:"values,omitempty"`
}

// Signal is one computed signal.
type Signal struct {
	Name       string     `json:"name"`
	Value      float64    `json:"value"` // 0–1
	Evidence   []Evidence `json:"evidence"`
	Confidence string     `json:"confidence"`
}

// Compute runs every signal over the input and returns them in a fixed
// order, one per name, values clamped to 0–1.
func Compute(in Input) []Signal {
	timelines := Build(in.Events)
	problems := make([]Problem, 0, len(in.Problems))
	for _, p := range in.Problems {
		if p.FinalSource == "" {
			if tl := timelines[p.ID]; tl != nil && tl.Reconstructed {
				p.FinalSource = tl.Source
			}
		}
		problems = append(problems, p)
	}
	in.Submissions = clockSubmissions(in.Events, in.Submissions)
	ctx := computation{in: in, timelines: timelines, problems: problems}
	sigs := []Signal{
		ctx.pasteRatio(),
		ctx.pasteThenPass(),
		ctx.burstTyping(),
		ctx.editRatio(),
		ctx.blurThenSolution(),
		ctx.speedVsDifficulty(),
		ctx.referenceSimilarity(),
	}
	for i := range sigs {
		sigs[i].Value = clamp01(sigs[i].Value)
		if sigs[i].Evidence == nil {
			sigs[i].Evidence = []Evidence{}
		}
		if sigs[i].Confidence == "" {
			sigs[i].Confidence = ConfidenceNormal
		}
		if in.Incomplete {
			sigs[i].Confidence = ConfidenceLow
		}
	}
	return sigs
}

// clockSubmissions moves each submission to the client time of the run or
// submit event that names it, so it sits on the same clock as the pastes
// and blurs it is measured against. One the stream never named keeps the
// server's time.
func clockSubmissions(events []Event, subs []Submission) []Submission {
	at := map[uuid.UUID]time.Time{}
	for _, ev := range events {
		if ev.Kind != "run" && ev.Kind != "submit" {
			continue
		}
		var data struct {
			SubmissionID uuid.UUID `json:"submission_id"`
		}
		if err := json.Unmarshal(ev.Payload, &data); err == nil && data.SubmissionID != uuid.Nil {
			at[data.SubmissionID] = ev.At()
		}
	}
	out := make([]Submission, len(subs))
	for i, sub := range subs {
		if t, ok := at[sub.ID]; ok {
			sub.At = t
		}
		out[i] = sub
	}
	return out
}

// Risk is the weighted sum of the signals, clamped to 0–100. Weights are
// points out of 100 (the org defaults sum to 100), so a signal at 1 adds its
// whole weight; weights that sum past 100 saturate rather than scale.
// Confidence does not enter the score: a gap in the recording is shown next
// to it, not hidden in it.
func Risk(sigs []Signal, weights map[string]float64) float64 {
	var sum float64
	for _, s := range sigs {
		sum += weights[s.Name] * clamp01(s.Value)
	}
	return math.Round(math.Max(0, math.Min(100, sum))*100) / 100
}

type computation struct {
	in        Input
	timelines map[uuid.UUID]*Timeline
	problems  []Problem
}

// orderedTimelines is the timelines in problem id order, so evidence is
// listed deterministically.
func (c computation) orderedTimelines() []*Timeline {
	out := make([]*Timeline, 0, len(c.timelines))
	for _, tl := range c.timelines {
		out = append(out, tl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProblemID.String() < out[j].ProblemID.String() })
	return out
}

func (c computation) finalLen(id uuid.UUID) int {
	for _, p := range c.problems {
		if p.ID == id {
			return len([]rune(p.FinalSource))
		}
	}
	if tl := c.timelines[id]; tl != nil && tl.Reconstructed {
		return len([]rune(tl.Source))
	}
	return 0
}

// pasteRatio is characters pasted from outside the page over the characters
// of the final source, across every problem.
func (c computation) pasteRatio() Signal {
	s := Signal{Name: PasteRatio}
	var pasted, final int
	for _, tl := range c.orderedTimelines() {
		final += c.finalLen(tl.ProblemID)
		for _, p := range tl.Pastes {
			if p.Internal {
				continue
			}
			pasted += p.Len
			s.Evidence = append(s.Evidence, Evidence{
				ProblemID: tl.ProblemID, At: p.At, Seq: p.Seq,
				Note:   fmt.Sprintf("%d characters pasted from outside the page", p.Len),
				Values: map[string]float64{"len": float64(p.Len)},
			})
		}
	}
	if final == 0 {
		s.Confidence = ConfidenceLow
		if pasted > 0 {
			s.Value = 1
		}
		return s
	}
	s.Value = float64(pasted) / float64(final)
	return s
}

// pasteThenPass fires when an external paste is followed by a passing
// submit of the same problem within the window.
func (c computation) pasteThenPass() Signal {
	s := Signal{Name: PasteThenPass}
	for _, tl := range c.orderedTimelines() {
		for _, p := range tl.Pastes {
			if p.Internal {
				continue
			}
			for _, sub := range c.in.Submissions {
				if sub.ProblemID != tl.ProblemID || sub.Kind != "submit" || !sub.Passed {
					continue
				}
				gap := sub.At.Sub(p.At)
				if gap < 0 || gap > PasteThenPassWindow {
					continue
				}
				s.Value = 1
				s.Evidence = append(s.Evidence, Evidence{
					ProblemID: tl.ProblemID, At: p.At, Seq: p.Seq,
					Note:   fmt.Sprintf("%d characters pasted, passing submit %s later", p.Len, gap.Round(time.Second)),
					Values: map[string]float64{"len": float64(p.Len), "seconds_to_pass": gap.Seconds()},
				})
				break
			}
		}
	}
	return s
}

// burstTyping is the share of typed characters that landed in runs faster
// than BurstRate sustained for longer than BurstMinDuration. Edits carrying
// a paste are not typing.
func (c computation) burstTyping() Signal {
	s := Signal{Name: BurstTyping}
	var typed, burst int
	for _, tl := range c.orderedTimelines() {
		var run []Edit
		flush := func() {
			if len(run) == 0 {
				return
			}
			var chars int
			for _, e := range run {
				chars += e.Inserted
			}
			dur := run[len(run)-1].At.Sub(run[0].At)
			if dur > BurstMinDuration && float64(chars)/dur.Seconds() > BurstRate {
				burst += chars
				s.Evidence = append(s.Evidence, Evidence{
					ProblemID: tl.ProblemID, At: run[0].At, Seq: run[0].Seq,
					Note:   fmt.Sprintf("%d characters in %s (%.1f chars/s)", chars, dur.Round(time.Second), float64(chars)/dur.Seconds()),
					Values: map[string]float64{"chars": float64(chars), "seconds": dur.Seconds()},
				})
			}
			run = run[:0]
		}
		for _, e := range tl.Edits {
			if e.FromPaste || e.Inserted == 0 {
				continue
			}
			typed += e.Inserted
			if len(run) > 0 && e.At.Sub(run[len(run)-1].At) > BurstGap {
				flush()
			}
			run = append(run, e)
		}
		flush()
	}
	if typed == 0 {
		s.Confidence = ConfidenceLow
		return s
	}
	s.Value = float64(burst) / float64(typed)
	return s
}

// editRatio is deletions over insertions in typed edits; composing code
// deletes as it goes, transcribing it does not, so the signal rises as the
// ratio falls under LowEditRatio.
func (c computation) editRatio() Signal {
	s := Signal{Name: EditRatio}
	var ins, del int
	for _, tl := range c.orderedTimelines() {
		for _, e := range tl.Edits {
			if e.FromPaste {
				continue
			}
			ins += e.Inserted
			del += e.Deleted
		}
	}
	if ins < MinTypedForEditRatio {
		s.Confidence = ConfidenceLow
		return s
	}
	ratio := float64(del) / float64(ins)
	s.Value = (LowEditRatio - ratio) / LowEditRatio
	if s.Value > 0 {
		s.Evidence = append(s.Evidence, Evidence{
			Note:   fmt.Sprintf("%d characters deleted against %d typed (ratio %.3f)", del, ins, ratio),
			Values: map[string]float64{"deleted": float64(del), "inserted": float64(ins), "ratio": ratio},
		})
	}
	return s
}

// blurThenSolution fires when the page was out of focus for longer than
// BlurMinDuration and at least BlurSolutionShare of the final source was
// inserted within BlurSolutionWindow of the return.
func (c computation) blurThenSolution() Signal {
	s := Signal{Name: BlurThenSolution}
	for _, tl := range c.orderedTimelines() {
		final := c.finalLen(tl.ProblemID)
		if final == 0 {
			continue
		}
		for _, b := range tl.Blurs {
			away := b.To.Sub(b.From)
			if away <= BlurMinDuration {
				continue
			}
			var inserted int
			for _, e := range tl.Edits {
				if !e.At.Before(b.To) && e.At.Sub(b.To) <= BlurSolutionWindow {
					inserted += e.Inserted
				}
			}
			share := float64(inserted) / float64(final)
			if share < BlurSolutionShare {
				continue
			}
			s.Value = 1
			s.Evidence = append(s.Evidence, Evidence{
				ProblemID: tl.ProblemID, At: b.From, Seq: b.Seq,
				Note: fmt.Sprintf("away %s, then %d%% of the final source within %s of returning",
					away.Round(time.Second), int(share*100), BlurSolutionWindow),
				Values: map[string]float64{"away_seconds": away.Seconds(), "share": share},
			})
		}
	}
	return s
}

// speedVsDifficulty is how much faster than the band median the first
// passing submit came, taking the fastest problem: 0 at or past the median,
// approaching 1 for an instant pass.
func (c computation) speedVsDifficulty() Signal {
	s := Signal{Name: SpeedVsDifficulty}
	for _, p := range c.problems {
		var first *Submission
		for i := range c.in.Submissions {
			sub := &c.in.Submissions[i]
			if sub.ProblemID == p.ID && sub.Kind == "submit" && sub.Passed && (first == nil || sub.At.Before(first.At)) {
				first = sub
			}
		}
		if first == nil {
			continue
		}
		start := c.in.StartedAt
		if tl := c.timelines[p.ID]; tl != nil && (start.IsZero() || tl.First.Before(start)) {
			start = tl.First
		}
		if start.IsZero() {
			s.Confidence = ConfidenceLow
			continue
		}
		median, ok := BandMedian[strings.ToLower(p.Difficulty)]
		if !ok {
			median = BandMedian["medium"]
		}
		took := first.At.Sub(start)
		value := 1 - took.Seconds()/median.Seconds()
		if value <= 0 {
			continue
		}
		if value > s.Value {
			s.Value = value
		}
		s.Evidence = append(s.Evidence, Evidence{
			ProblemID: p.ID, At: first.At,
			Note:   fmt.Sprintf("first pass after %s on a %s problem (band median %s)", took.Round(time.Second), p.Difficulty, median),
			Values: map[string]float64{"seconds": took.Seconds(), "median_seconds": median.Seconds()},
		})
	}
	return s
}

// referenceSimilarity is the highest normalized token similarity between the
// final source and the problem's references or other candidates' submits.
func (c computation) referenceSimilarity() Signal {
	s := Signal{Name: ReferenceSimilarity}
	var compared bool
	for _, p := range c.problems {
		if p.FinalSource == "" {
			continue
		}
		own := shingles(p.FinalSource)
		if len(own) == 0 {
			continue
		}
		var best float64
		var against string
		for _, ref := range p.References {
			compared = true
			if sim := dice(own, shingles(ref)); sim > best {
				best, against = sim, "reference solution"
			}
		}
		for _, other := range p.Others {
			compared = true
			if sim := dice(own, shingles(other)); sim > best {
				best, against = sim, "another candidate's submission"
			}
		}
		if against == "" {
			continue
		}
		if best > s.Value {
			s.Value = best
		}
		s.Evidence = append(s.Evidence, Evidence{
			ProblemID: p.ID,
			Note:      fmt.Sprintf("%.0f%% similar to %s", best*100, against),
			Values:    map[string]float64{"similarity": best},
		})
	}
	if !compared {
		s.Confidence = ConfidenceLow
	}
	return s
}

var tokenRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|\d+(?:\.\d+)?|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\sA-Za-z0-9_]`)

// shingleSize is the token n-gram the similarity is measured over; three
// tokens is long enough that shared idioms do not match on their own.
const shingleSize = 3

// shingles tokenizes source and returns its token trigram multiset with
// identifiers normalized to one placeholder, so renaming variables does not
// hide a copy, while keywords, literals, and structure still have to match.
func shingles(src string) map[string]int {
	raw := tokenRe.FindAllString(src, -1)
	tokens := make([]string, 0, len(raw))
	for _, t := range raw {
		switch {
		case keywords[t]:
			tokens = append(tokens, t)
		case t[0] == '_' || (t[0] >= 'a' && t[0] <= 'z') || (t[0] >= 'A' && t[0] <= 'Z'):
			tokens = append(tokens, "$id")
		default:
			tokens = append(tokens, t)
		}
	}
	out := map[string]int{}
	if len(tokens) < shingleSize {
		if len(tokens) > 0 {
			out[strings.Join(tokens, " ")]++
		}
		return out
	}
	for i := 0; i+shingleSize <= len(tokens); i++ {
		out[strings.Join(tokens[i:i+shingleSize], " ")]++
	}
	return out
}

// keywords is the union of the supported languages' reserved words, kept as
// themselves when normalizing identifiers.
var keywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`
		and as assert async await break case catch class const continue def default del delete do elif else
		enum except export extends false finally for from func function global go if import in interface is
		lambda let new nil none not null or package pass raise return select self static struct switch this
		throw true try type typeof var void while with yield
		SELECT FROM WHERE GROUP BY ORDER HAVING JOIN LEFT RIGHT INNER OUTER ON AS AND OR NOT NULL IN LIMIT
		select from where group by order having join left right inner outer on limit distinct count sum avg min max
		int str len range print map filter println fmt`) {
		m[k] = true
	}
	return m
}()

// dice is the Sørensen–Dice coefficient of two multisets.
func dice(a, b map[string]int) float64 {
	var na, nb, shared int
	for _, n := range a {
		na += n
	}
	for k, n := range b {
		nb += n
		if m, ok := a[k]; ok {
			shared += min(m, n)
		}
	}
	if na+nb == 0 {
		return 0
	}
	return 2 * float64(shared) / float64(na+nb)
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}
