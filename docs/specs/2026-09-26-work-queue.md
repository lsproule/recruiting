# Work queue: every step, every client, on one screen

## What it is for

A recruiting agency runs the same process for every client: someone applies,
their résumé is read, a call is booked, feedback is filed, an exam goes out,
the exam is reviewed, the candidate is forwarded, the client shortlists by
round-robin sprint or targeted interviews, interviews are held and rated,
further rounds happen, and a decision is made. At any moment each candidate
is waiting on exactly one of those steps, and most of the waiting is on a
person in the agency.

The work queue bubbles that up: for every client at once, the next thing a
person has to do for each candidate, most overdue first, with the decision
on the row where the step allows one. Anything the platform can do on its
own (tell a rejected candidate, advance or reject on an exam score, put an
interview in both calendars) it does, so the queue holds only what needs a
person.

## The steps and their rules

The queue is a read model: one scan of the open applications
(`ListQueueSteps`) plus the rules that live outside a candidate's stage. Each
open application is asked what it is waiting for, by the kind of stage it
sits in and what that stage has collected:

| Step | Rule (`QueueKind`) | Fires when | Due | Resolved by |
| ---- | ------------------ | ---------- | --- | ----------- |
| 1 | `resume` | The application sits in the first stage | 48h after applying | **Advance** or **Reject** on the row |
| 2 | `call_unbooked` | In an interview stage with no booked slot | 72h after entering | The candidate booking; the row links to the application to chase or re-invite |
| 3 | `feedback` | The slot has ended and no scorecard is filed | The moment it ends | The scorecard link |
| 4 | `exam_unopened` | The attempt is invited and unopened | The invite's expiry | The candidate opening it; a lapsed invite becomes a decision (re-invite or reject) |
| 5 | `exam_review` | The attempt is scored and has no verdict | 24h after finishing | The review link |
| 5 | `forward` | In a client stage and not released | 24h after entering | **Forward to client** on the row |
| 5 | `client_waiting` | The client asked a question nobody answered | When asked | The application page |
| 5 | `client_silent` | Released three days ago and the client has not touched it | Three days after release | A nudge; the row links to the application |
| 6 | `shortlist_plan` | In a sprint stage and in no sprint | 48h after entering | Planning a round-robin sprint or booking targeted interviews instead |
| 6 | `sprint_rating` | A sprint conversation ended unrated | Five minutes after it ends | The interviewer's rating |
| 6 | `shortlist_draft` | A shortlist packet was built and not sent | When last edited | The builder |
| 7 | `decision` | The stage has what it needs (a scorecard, a verdict, a rating) or an exam lapsed | 48h after it got it | **Advance** or **Reject** on the row |
| 9 | `talent_intro` | A company asked to meet a talent network match | 48h after the ask | Sending the opportunity |

Nothing fires while the ball is in someone else's court: a booked call that
has not happened, an exam in progress, a client who has the candidate and is
engaging. A row disappears the moment the work behind it is done, because
nothing is stored: the queue is derived every time it is read. Snoozes
(24h, per user) are the one thing it stores.

The screen groups rows by client, so the recruiter reads "for Globex, these
five things", and filters by step with chips that show only the steps with
something waiting.

## Decisions on the row

`POST /app/queue/decide` takes `application_id`, `action`
(`advance` | `reject` | `release`), `to_stage_id`, and `reason`. Advance and
reject go through `ApplicationService.Move`, so every rule that applies on
the board applies here: a reject needs a reason, a stage's prerequisite must
be met, a stale row is refused with the reason shown as a flash. Release
goes through the release service, notice to the client included.

## Automations

- **Rejection email.** Closing an application as rejected, by anyone or
  anything, emails the candidate (`application_rejected`) unless the org
  turns `rejection_email` off in its settings. The note says only that the
  application is closed; the reason on the timeline stays internal.
- **Assessment stages decide on their score.** An assessment stage may carry
  a pass mark out of 100 with `auto_advance` and/or `auto_reject`. When the
  sitting is scored, at or above the mark with auto-advance on the
  application moves to the next stage; below it with auto-reject on it is
  closed as rejected (and the candidate gets the email). The move is the
  system's, recorded on the timeline with the score and the mark as its
  reason, in the same transaction as the score. A stage with neither leaves
  every verdict to a person. The settings live on template stages too, so a
  process carries them into every job.
- **Calendar entries for interviews.** Both booking confirmations, the
  candidate's and the interviewer's, attach the interview as an `.ics` file
  and carry an "add to Google Calendar" link. The event's UID is the
  application and stage, so a rescheduled interview replaces the earlier
  entry.

## Not in scope

Reminders to candidates who have not booked, and nudges to silent clients,
are still sent by hand from the application page; the queue only says they
are due. Job postings to external boards are their own feature.
