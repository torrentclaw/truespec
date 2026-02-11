# Contributing to TrueSpec

Thanks for your interest in contributing! Here's how you can help.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/<your-user>/truespec.git`
3. Create a branch: `git checkout -b feat/my-feature`
4. Make your changes
5. Test locally: `go build ./cmd/truespec`
6. Commit with a clear message (see below)
7. Push and open a Pull Request

## Requirements

- Go 1.25+
- `ffprobe` installed (from FFmpeg)

## Commit Messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add new audio codec detection
fix: correct HDR10 detection for MKV files
docs: update README examples
refactor: simplify piece selection logic
```

## Code Style

- Run `gofmt` before committing
- Keep functions focused and small
- Add comments only where the logic isn't self-evident

## Reporting Bugs

Open an issue with:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Your OS and Go version

## License

By contributing, you agree that your contributions will be licensed under the project's [GNU Affero General Public License v3.0](LICENSE).
