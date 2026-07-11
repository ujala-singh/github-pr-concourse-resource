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
│  │       └───────┬───────┴───────┬───────┘                  │  │
│  │               │               │                          │  │
│  │        Detects source.number  │                          │  │
│  │               │               │                          │  │
│  │       ┌───────┴────┬──────────┴─────┐                    │  │
│  │       ▼            ▼                ▼                    │  │
│  │  ┌─────────┐  ┌─────────┐    ┌──────────┐                │  │
│  │  │ prlist/ │  │   pr/   │    │   pr/    │                │  │
│  │  │  check  │  │  check  │    │   out    │                │  │
│  │  └────┬────┘  └────┬────┘    └────┬─────┘                │  │
│  │       │            │              │                      │  │
│  │  ┌─────────┐  ┌─────────┐    ┌──────────┐                │  │
│  │  │ prlist/ │  │   pr/   │    │          │                │  │
│  │  │   in    │  │   in    │    │   out    │                │  │
│  │  └────┬────┘  └────┬────┘    └────┬─────┘                │  │
│  │       │            │              │                      │  │
│  └───────┼────────────┼──────────────┼──────────────────────┘  │
│          │            │              │                         │
│          └────────────┴──────────────┘                         │
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
   │ - Metadata   │     │ - Clone repo │
   │   only       │     │ - Merge/     │
   │ - Instance   │     │   Rebase     │
   │   pipelines  │     │ - Status     │
   │              │     │   updates    │
   └──────────────┘     └──────────────┘


                Data Flow
          ==================

Check (prlist):
  User Config → prlist.Check() → models.GetPullRequests() 
    → GitHub GraphQL → Filter PRs → Return Versions

Check (pr):
  User Config → pr.Check() → models.GetPullRequestCommits()
    → GitHub REST → Filter Commits → Return Versions

In (prlist):
  Version → prlist.In() → models.GetPullRequest()
    → Write Metadata Files → Return Metadata

In (pr):
  Version → pr.In() → Clone Repo → Fetch PR 
    → Merge/Rebase → Write Metadata → Return Metadata

Out (pr only):
  Params → pr.Out() → models.UpdateCommitStatus()
    → models.AddComment() → Return Version
```

## Key Design Principles

1. **Single Responsibility**: Each package has one clear purpose
2. **Mode Isolation**: PR list and single PR logic are completely separate
3. **Shared Foundation**: Common GitHub client and models reused
4. **Immutable Operations**: All git operations create new state
5. **Fail Fast**: Validation happens early at configuration load
6. **Clean Errors**: Descriptive error messages with context
7. **Testing**: All core logic is testable without GitHub access
