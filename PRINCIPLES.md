# Engineering Principles

Law for agents working on this codebase. Version 2026-08-30.

## Working together

**Explicit instructions override these principles.** When the engineer asks for
something that contradicts a rule here, say so once and then do what was asked.

**Ground yourself in the source before you act.** Read the actual file, schema, or
documentation rather than reasoning from what you remember or expect it to say. Check
the code and local docs first; go to the web when they do not answer it.

**Show the shape before you build on it.** Models, schemas, interfaces, and the
contract between two pieces get reviewed — one at a time — before there is code
depending on them.

**Own the full loop: test, diagnose, fix, re-run.** Stop for elevated privilege, for
human review, or for a decision you cannot resolve without assuming — not for
mechanical work you can verify yourself. Once a gate clears, resume without re-asking.

**Start research narrow.** One or two searches answering a specific question. Before
widening into a broader sweep or a parallel fan-out, say which questions it is meant
to answer and what context it is meant to fill, then ask.

**Never build on an assumption.** When a decision or open question has downstream
impact — and most do — stop and escalate to the engineer rather than choosing an
answer and proceeding.

**Automate every mechanical decision.** Formatting, import order, and style are settled
by tooling and never discussed in review.

**Create only the files you were asked for.** No summary documents, no notes files, no
README alongside the work unless it was requested.

**Read your own diff before handing it over.** Revert anything you changed that is not
part of the work — leftover debug output, incidental reformatting, edits made while
exploring. The diff contains only the change that was asked for.

**Close review gaps before merge; never bank them as debt.** That includes Minor
findings and "out of scope" items of the same class as something already being fixed.
Surface the findings that need a decision, and keep driving the rest until every
finding is closed. Deferring anything needs approval, and what is deferred gets
committed to the project's canonical location — never a memory.

## Communication

**Write in plain English, concisely.** No jargon, no filler, no restating the question
back. Describe what something actually does rather than naming the machinery that does
it. This applies to everything you write — replies, comments, commit messages, docs.

**Ask exactly one focused question per turn.** Never batch sub-questions into one
message. When several things need confirming, state the settled ones back in a
sentence each and ask only the one that is genuinely open.

**Say when you don't know.** State uncertainty plainly rather than producing a
confident answer you cannot support.

**Interrupt only for something that actually needs the engineer.** Progress updates and
status reports are not worth breaking someone's attention for.

## Architecture

**Choose dependencies by research, never from memory.** A dependency is a legitimate
option — weigh it against writing the thing yourself rather than defaulting to either.
Before adopting one, web-research it: find the real alternatives and compare them on
fit and repository health — maintenance activity and who backs it. Never rely on model
memory for a library's existence, API, or version. Default to the latest stable major
line, confirmed by research — never a pre-release, and never a major version you
already know you will migrate off.

**Research established practice before solving a problem yourself.** Assume the
problem is already solved and go find out how, rather than reasoning it out from
scratch. Build it well rather than fast.

**Add indirection only when a second case demands it.** Abstractions, layers,
extension seams, and configuration points exist to serve more than one caller. Until
the second caller exists, prefer a direct implementation.

**One name, one thing — everywhere.** A name chosen for a concept, table, field, or
model is used consistently across code, database, API, and documentation, and no two
concepts share it. A rename is applied everywhere it appears, accepting the churn and
migrations that requires.

**Name by meaning, never by process artifact.** Requirement numbers, ticket IDs, phase
labels, and step numbers belong in planning documents and git history, never in code.

**Prove the risky path end-to-end before building on it.** When a design depends on an
integration you have not exercised, spike it — build the thinnest thing that proves it
works, then build the feature on top. Spikes are encouraged, and they are throwaway:
they are not held to the standards of code that ships.

**Build modules by purpose, and let each own its concern.** A module is defined by
what it is for, never by which layer it sits in. Expose the smallest surface its
callers need and keep the rest internal.

**When two things must agree, derive both from one source.** Never configure the same
value in two places and rely on them matching. Where the sources genuinely cannot be
merged, lock them together with a conformance test.

**Keep the dependency graph explicit and acyclic.** No cycles, no scattered
cross-imports. Enforce module boundaries with the language and the linter, not by
convention.

**Keep logic free of I/O.** Rules and decisions live in pure functions; network,
filesystem, and database access lives at the edges and is passed in.

**Backwards compatibility requires explicit approval.** Change the thing and update
its callers. Raise a compatibility concern if you see one, but never plan or implement
a deprecation shim, dual-path support, or a migration step without being told to.

**One write path per domain.** All writes to a given piece of state go through a
single module, and every caller uses it. Never add a second path that writes the same
state.

## Types

**Every boundary has a typed model.** API, config, database row, external payload —
each has an explicit typed model (Pydantic, Zod, or the language's equivalent), and
interior code trusts it. Database tables follow the same rules.

**Never weaken a type for convenience.** Never cast, widen, or `any` past a type error;
fix the type or fix the value. Type violations are lint errors and get fixed, never
suppressed — a cast, an ignore, or a suppression requires the engineer's explicit
approval and an inline comment saying why.

**Null and undefined are opt-in.** Nothing is nullable unless declared so, and an
optional value is null — never `0`, `""`, or `false`. `NOT NULL` is the database
default.

## Failure

**Fail loudly; an unproven case is not a handled case.** Unknown input, unexpected
state, or any path you have not actually observed stops with a clear error — never a
default, never a fallback, never a substituted value. Do not write branches for
scenarios you cannot demonstrate happen. Every swallowed error and every "continuing"
branch is a failure and gets logged as one.

**An unexpected partial result is a failure, not a result.** When some of the work
cannot be completed, report the whole run as failed rather than returning what
succeeded. Never drop the items you could not handle, and never decide on your own
that a partial result is acceptable — that call belongs to the engineer.

**Exceptions are specifically typed and carry their context.** Name the exception for
what actually went wrong and attach the message and data a reader needs. Never thread
extra arguments through a function only to enrich its error — wrap the exception where
the context already exists.

**Catch the expected case specifically; never let it reach the log as a failure.** A
known, harmless condition is handled by name. Anything that surfaces as an error is a
real failure.

## Observability

**Design logging on day one.** Decide what to log while designing the thing, not after
it breaks. Ask what a future debugger would need and log that: key events, state
transitions, decisions, and failures with their context. Every logged exception
carries its stack trace. Choose the level deliberately. Do not narrate normal
execution.

**Contextual fields are attached by the platform, not the call site.** Session id, job
id, request id and their equivalents are bound once at the architectural level so no
individual log call has to carry them — and they are structured fields, never
interpolated into the message.

## Testing

**A test that cannot fail is not a test.** Prove every test by breaking the system and
watching it go red; a test that has never failed proves nothing. Assert that the work
happened, not that the call returned successfully.

**Build and test each piece before starting the next.** Write its tests as you write
it, exercise its surface, and confirm it works before moving on. Never build a series
of pieces and leave the testing until the end.

**Test at the layer that can actually catch the defect, and accept the complexity that
requires.** Unit, component, integration, end-to-end, database — each layer catches
what the others structurally cannot. Do not collapse layers to keep the suite simple,
and do not skip a layer because standing it up is work.

**Tests are hermetic.** Replay committed fixtures rather than calling live services,
and assert a signal that proves nothing reached the network — a test that can silently
fall back to a live call has stopped being a reproduction.

**Flake is a bug.** A test that passes and then fails without a code change gets
diagnosed, not retried. Never paper over it with sleeps, delays, or extra polling.
Test code is held to the same standard as production code.

**Write conformance tests for rules nothing else can enforce.** When a convention or
invariant holds across the codebase but no linter or type checker can check it, write
a test that inspects the codebase and asserts it.

**Contract-test what an upstream does not guarantee.** When you rely on undocumented
or incidental behavior, confine it to one module and write a test that fails when it
changes. A documented public API is taken at face value — except where its behavior
has surprised you before, in timing, ordering, or some idiosyncrasy, in which case pin
that specific behavior.

**Pin an upstream bug with a test that asserts the broken behavior.** When you work
around a defect in a dependency, lock the defect in a test — upstream fixing it then
fails loudly instead of silently leaving the workaround behind. Where the issue is
tracked upstream, put its URL in a comment.

**Look at the real output, don't just assert on it.** Take the screenshot, render the
page, run the command and read what it produced. Judge what you can judge yourself, and
escalate to the engineer only what genuinely needs a human eye.

## Security

**Enforce access control at the data layer, at every entry point.** Verify both
authentication and ownership where the data is read or written; never assume an
upstream layer checked. A middleware redirect is UX, not access control.

**Security requirements come from research, not instinct.** Web-research current best
practice for the surface you are building, and turn it into explicit requirements with
tests that prove them.

**Security review is independent and covers specs as well as code.** Run it with the
strongest model available, grounded in researched best practice, against the plans and
the implementation both.

**Probe the real public surface adversarially.** Drive the deployed surface the way an
attacker would; a passing test suite is not a security check.

**A security gate fails closed.** When the check cannot run — the service is down, the
config is missing, the token cannot be verified — deny. Never fall through to allow,
even when the framework around it defaults to open. Any exception requires the
engineer's explicit approval and a comment in the code or config saying why.

## Changing code

**Follow the patterns already in the codebase.** New code matches the established
pattern. A discrepancy is a bug — fix it or raise it, never add a second way of doing
the same thing.

**Fix every instance, not just the one you were shown.** After a correction, find the
other places the same problem appears, fix them, and deal with whatever allowed them to
differ.

**Never remove an existing guard, workaround, or check you cannot explain.** Code that
looks unnecessary is load-bearing until you have established otherwise.

**Prove every guard can fire.** A check that has never rejected anything is redundant
or wrongly conditioned. Trigger it deliberately and confirm it rejects; if it cannot,
delete it. A test locks the behavior — a second guard does not.

**Delete dead code.** Unused functions, commented-out blocks, and unreachable branches
come out. Git history holds anything you need back.

## Debugging

**Preserve broken state.** When something breaks, gather read-only diagnostics and
stop. Do not restart, clear, reset, or re-run to get back to green — the live broken
state is the evidence, and recovering destroys it.

**Diagnose before you fix; a hypothesis is not a diagnosis.** Never add delays,
retries, or extra polling to make a failure go away — an unproven timing fix is cruft
that outlives the bug it was guessing at. Add temporary diagnostics, establish the
actual mechanism, then fix it and remove them. When attempts stop converging on a
cause, stop and escalate for guidance rather than trying repeatedly.

**When a fix appears to do nothing, check that what you changed is what ran.** Suspect
a stale build, a cached artifact, or an installed copy before suspecting the fix.

## Comments

**Comments explain why, not what.** A comment earns its place by naming a non-obvious
constraint, a tricky mechanism, or something that looks wrong but is correct. Never
restate what the code already says, and keep it brief. Never record what you used to
believe or how the understanding evolved — git history holds that.

**A comment that contradicts the code is a bug.** When you change code, update or
delete the comments around it.

## Documentation

**One authoritative source per fact; everything else points at it.** Never restate in
a second document what one document already owns — link to it instead.

**Every document is living or frozen.** A living document must be true right now;
drift in one is a bug and gets fixed the moment it is found. A frozen document — a
plan, a spec, a decision record — states what was true when it was written; it is
superseded, never corrected.

**Never put project knowledge in a memory.** Anything worth keeping goes in the
repository, where it is versioned, reviewed, and visible. Memories are not a place for
decisions, conventions, or state.
