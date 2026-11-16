# Contributing to TailConnect

Thanks for your interest in TailConnect! Bug reports, ideas and pull requests are all welcome.

## Reporting bugs

[Open an issue](https://github.com/imbikramsaha/TailConnect/issues/new) with:

- your operating system and TailConnect version (**Settings → About**, or `tailconnect version`),
- what you did, what you expected and what happened instead,
- logs if possible: **Settings → About → Report a problem** creates a zip on your computer. Look through it before attaching it.

**Security issues:** please don't open a public issue. Use [private vulnerability reporting](https://github.com/imbikramsaha/TailConnect/security/advisories/new) instead — see [SECURITY.md](SECURITY.md).

## Suggesting features

Open an issue describing the problem you want to solve, not only the solution. It helps to know how you use TailConnect today.

## Pull requests

1. For anything larger than a small fix, open an issue first so we can agree on the approach.
2. Read [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) to build and test locally.
3. Keep changes focused, and add tests for new behaviour — especially for anything that handles data from other devices.
4. Before pushing, run:

   ```sh
   gofmt -l . && make vet && make test
   ```

5. Describe what changed and how you tested it, including which operating systems.

CI runs the tests on macOS, Windows and Linux, plus CodeQL and `govulncheck`. A pull request needs all checks to pass.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
