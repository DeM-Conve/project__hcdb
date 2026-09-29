# Contributing to hcdb

hcdb uses **trunk-based development**. `main` is the trunk: the single source of truth, always
green, always releasable. There are no long-lived `dev`, `release`, or feature branches.

## Branching model

- `main` is the only long-lived branch. It must always build and pass `go test -race ./...`.
- Work in **small increments** that merge back to `main` quickly, ideally within a day and
  never more than a couple of days. Rebase on `main` often.
- Short-lived branches are named by type: `feat/<topic>`, `fix/<topic>`, `docs/<topic>`,
  `chore/<topic>`, `refactor/<topic>`, `test/<topic>`.
- Maintainers may commit small, low-risk changes (docs, typos) directly to `main`. Everything
  else goes through a pull request so CI runs first.
- Big features land as a series of small PRs. Incomplete work stays behind a config flag or
  unexported/unwired code, so `main` stays shippable at every commit.
- PRs are **squash-merged** (or rebased) to keep `main` history linear. The branch is deleted
  on merge.

## Workflow

1. Fork the repo (external contributors) or branch from the latest `main` (maintainers):
   ```
   git switch main && git pull --rebase
   git switch -c feat/my-change
   ```
2. Make a focused change. Run the same checks CI runs:
   ```
   gofmt -l .          # must print nothing
   go vet ./...
   go test -race ./...
   ```
3. Push and open a pull request against `main`. Keep it small enough to review in one sitting.
4. Once CI is green and review is done, squash-merge and delete the branch:
   ```
   git push origin --delete feat/my-change
   git branch -d feat/my-change
   ```

## Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`,
e.g. `feat(wal): add fsync batching`. Common types: `feat`, `fix`, `docs`, `chore`, `refactor`,
`test`, `perf`. Since PRs are squash-merged, the **PR title** becomes the commit on `main`, so it
must follow this format too.

## Releases

Releases are annotated tags cut directly from `main` using
[semantic versioning](https://semver.org/) (`vX.Y.Z`). There are no release branches. If a fix
is needed, it lands on `main` and a new patch tag is cut.

```
git tag -a v1.1.0 -m "v1.1.0"
git push origin v1.1.0
```

Update [CHANGELOG.md](CHANGELOG.md) in the same PR as the change, under `## [Unreleased]`.
