# cloud-boot docs

mkdocs source for `https://cloud-boot.github.io/docs/`. Built with
[mkdocs-material](https://squidfunk.github.io/mkdocs-material/) and
deployed in versioned form by [mike](https://github.com/jimporter/mike)
via [`.github/workflows/pages.yml`](../.github/workflows/pages.yml).

## Release model

| Trigger | Version | Aliases | Default |
| --- | --- | --- | --- |
| Push to `main` | `dev` | — | unchanged |
| Push tag `v<X.Y.Z>` | `<X.Y.Z>` | `latest` | `latest` |
| Manual `workflow_dispatch` | (input) | (input) | only if `latest` listed |

So the day-to-day flow is:

```sh
# develop on main — every push refreshes /docs/dev/
git push origin main

# cut a release — every tag deploys + promotes 'latest'
git tag  v0.2.0
git push origin v0.2.0
```

The version dropdown in the top bar of the material theme is wired
to mike via `extra.version.provider: mike` in [`mkdocs.yml`](mkdocs.yml).

## Local preview (single-version)

```sh
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
mkdocs serve -f mkdocs.yml   # http://127.0.0.1:8000
```

## Local preview (versioned, like production)

```sh
# from inside docs/
mike deploy --deploy-prefix docs 0.1.0 latest
mike set-default --deploy-prefix docs latest
mike serve --deploy-prefix docs   # serves /docs/... with the dropdown
```

## Manually deploying out of band

Releases trigger automatically on tag push. If you ever need to
re-deploy a specific version by hand:

```sh
mike deploy --push --update-aliases --deploy-prefix docs 0.2.0 latest
mike set-default --push --deploy-prefix docs latest
```
