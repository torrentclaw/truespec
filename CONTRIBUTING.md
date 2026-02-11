# Contributing to TrueSpec

Thanks for your interest in contributing! Here's how you can help.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/<your-user>/truespec.git`
3. Install dev tools and git hooks:
   ```bash
   make install-tools
   make hooks
   ```
4. Create a branch: `git checkout -b feat/my-feature`
5. Make your changes
6. Test locally: `make build && make test`
7. Commit with a clear message (see below) — the commit-msg hook will validate the format
8. Push and open a Pull Request

## Requirements

- Go 1.25+
- `ffprobe` installed (from FFmpeg)
- [lefthook](https://github.com/evilmartians/lefthook) (installed via `make install-tools`)

## Commit Messages

Commits are validated automatically by a git hook. We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>[optional scope][!]: <description>
```

Valid types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`

Examples:
```
feat: add new audio codec detection
fix(scanner): correct HDR10 detection for MKV files
docs: update README examples
refactor: simplify piece selection logic
feat!: redesign output format
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
