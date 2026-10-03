# Architecture Diagram

```
┌────────────────────────────────────────────────────────────────┐
│                    GitHub PR Concourse Resource                │
│                                                                │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                    Entry Points                          │  │
│  │                                                          │  │
│  │  ┌──────────┐    ┌──────────┐    ┌──────────┐            │  │
│  │  │  check   │    │    in    │    │   out    │            │  │
│  │  │ (cmd/)   │    │ (cmd/)   │    │ (cmd/)   │            │  │
│  │  └────┬─────┘    └────┬─────┘    └────┬─────┘            │  │
│  │       │               │               │                  │  │
│  │       └───────┬───────┘               │                  │  │
│  │               │                       │                  │  │
│  │        Detects source.number    No mode dispatch —       │  │
│  │               │                 always calls pr.Out,     │  │
│  │       ┌───────┴────┐             both modes share it     │  │
│  │       ▼            ▼                  │                  │  │
│  │  ┌─────────┐  ┌─────────┐        ┌──────────┐            │  │
│  │  │ prlist/ │  │   pr/   │        │   pr/    │            │  │
│  │  │  check  │  │  check  │        │   out    │            │  │
│  │  └────┬────┘  └────┬────┘        └────┬─────┘            │  │
│  │       │            │                  │                  │  │
│  │  ┌─────────┐  ┌─────────┐             │                  │  │
│  │  │ prlist/ │  │   pr/   │             │                  │  │
│  │  │   in    │  │   in    │             │                  │  │
│  │  └────┬────┘  └────┬────┘             │                  │  │
│  │       │            │                  │                  │  │
│  └───────┼────────────┼──────────────────┼──────────────────┘  │
│          │            │                  │                     │
│          └────────────┴──────────────────┘                     │
│                       ▼                                        │
│          ┌────────────────────────┐                            │
│          │     models/            │                            │
│          │                        │                            │
│          │  ┌──────────────────┐  │                            │
│          │  │  GithubClient    │  │                            │
│          │  │                  │  │                            │
│          │  │  - V3 (REST)     │  │                            │
│          │  │  - V4 (GraphQL)  │  │                            │
│          │  │                  │  │                            │
│          │  │  Methods:        │  │                            │
│          │  │  - GetPRs        │  │                            │
│          │  │  - GetPR         │  │                            │
│          │  │  - GetCommits    │  │                            │
│          │  │  - UpdateStatus  │  │                            │
│          │  │  - AddComment    │  │                            │
│          │  │  - CheckTrigger- │  │                            │
│          │  │    Comments      │  │                            │
│          │  └──────────────────┘  │                            │
│          │                        │                            │
│          │  Configuration Types:  │                            │
│          │  - CommonConfig        │                            │
│          │  - GithubConfig        │                            │
│          │  - Version             │                            │
│          │  - Metadata            │                            │
│          └────────┬───────────────┘                            │
│                   │                                            │
└───────────────────┼────────────────────────────────────────────┘
                    ▼
          ┌─────────────────────┐
          │   GitHub API        │
          │                     │
          │  REST API (v3)      │
          │  GraphQL API (v4)   │
          └─────────────────────┘


                Mode Detection Flow
          ================================

          ┌─────────────────────┐
          │   Input Request     │
          └──────────┬──────────┘
                     │
                     ▼
          ┌──────────────────────┐
          │ Has source.number?   │
          └──────────┬───────────┘
                     │
          ┌──────────┴──────────┐
          │                     │
          ▼                     ▼
     ┌────────┐           ┌────────┐
     │   NO   │           │  YES   │
     └────┬───┘           └────┬───┘
          │                    │
          ▼                    ▼
   ┌──────────────┐     ┌──────────────┐
   │  PR List     │     │  Single PR   │
   │  Mode        │     │  Mode        │
   │              │     │              │
   │ - Track all  │     │ - Track one  │
   │   PRs        │     │   PR commits │
   │ - Clone repo │     │ - Clone repo │
   │   by default │     │   + merge/   │
   │   (skip with │     │   rebase     │
   │   params:    │     │              │
   │  skip_down-  │     │              │
   │  load: true) │     │              │
   │ - Feeds      │     │              │
   │   instance   │     │              │
   │   pipelines  │     │              │
   └──────────────┘     └──────────────┘

Status updates and comments (put/out) are available in BOTH modes —
cmd/out has no mode dispatch (see diagram above).


                Data Flow
          ==================

Check (prlist):
  User Config → prlist.Check() → models.GetPullRequests()
    → GitHub GraphQL → Filter PRs by path (concurrent, bounded pool;
      diffs from the last known commit when this PR IS the resource's
      tracked cursor, else falls back to the PR's full base...HEAD diff
      — see MatchesPathFilters)
    → [if trigger_comments set] scan each matching PR's comments
      (concurrent, bounded pool) → Return Versions

Check (pr):
  User Config → pr.Check() → models.GetPullRequestCommits()
    → GitHub REST → Filter Commits (diffs from the last known commit —
      always available in this mode — to the new HEAD, via
      MatchesPathFilters / GetChangedFilesSince)
    → [if trigger_comments set] scan this PR's comments → Return Versions

In (prlist):
  Version → prlist.In() → Clone Repo → Fetch PR
    → Write Metadata Files → Return Metadata
  (params.skip_download skips the clone, and writes only pr/url/head_sha —
   not title/author)

In (pr):
  Version → pr.In() → Clone Repo → Fetch PR 
    → Merge/Rebase → Write Metadata → Return Metadata

Out (always pr.Out, both modes — cmd/out has no mode dispatch):
  Params → pr.Out() → read version.json (recover comment watermark)
    → models.UpdateCommitStatus() → models.AddComment() → Return Version
```

## Key Design Principles

1. **Single Responsibility**: Each package has one clear purpose
2. **Mode Isolation**: PR list and single PR logic are completely separate
3. **Shared Foundation**: Common GitHub client and models reused
4. **Immutable Operations**: All git operations create new state
5. **Fail Fast**: Validation happens early at configuration load
6. **Clean Errors**: Descriptive error messages with context
7. **Testing**: All core logic is testable without GitHub access
