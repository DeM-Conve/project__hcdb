# Contributing to hcdb

hcdb uses [GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow) for branching. It's simple: `main` is always deployable, and all work happens on short-lived branches merged back via pull request.

## Branching model

- `main` is the only long-lived branch. It should always build and pass tests.
- Never commit directly to `main`. All changes land through a pull request.
- Cut a new branch from `main` for any change: a feature, a fix, docs, whatever.
- Name branches by type and topic: `feat/<topic>`, `fix/<topic>`, `chore/<topic>`, `docs/<topic>`.
- Push your branch and open a PR against `main` as soon as it's ready for feedback.
- Once a PR is approved and merged, delete the branch. Don't let merged branches linger on the remote.

## Workflow

1. Fork the repo (external contributors) or create a branch (maintainers):
   ```
   git checkout -b feat/my-change main
   ```
2. Make your changes, with focused commits.
3. Push and open a pull request against `main`.
4. Address review feedback by pushing more commits to the same branch.
5. Once merged, delete the branch:
   ```
   git push origin --delete feat/my-change
   ```

## Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`, e.g. `feat(wal): add fsync batching`. Common types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`.

## Releases

Tag releases on `main` using [semantic versioning](https://semver.org/) (`vX.Y.Z`). There is no separate release branch — releases are tags on `main`.
