# Commit-message rules

Shared by `/commit-compose` (claude-recompose and claude-reword groups) and
`/cc-commit`. Every commit message either skill proposes MUST follow these
rules.

## Format

Use Conventional Commits:

```
<type>(<scope>): <summary>
```

For a breaking change, add `!` before the colon: `<type>(<scope>)!: <summary>`.

## Allowed types

`feat`, `fix`, `refactor`, `chore`, `docs`, `test`, `ci`, `build`,
`perf`, `revert`.

## Scope (this is the load-bearing part)

- Always include a scope when one is clear.
- Scope describes the **affected project, module, feature, directory,
  service, chart, role, package, or functional area** - what the change
  is about, not how it is implemented.
- Scope should NOT describe only the implementation technology unless
  the change is about the technology itself, its shared configuration,
  tooling, or repository-wide setup.
- Avoid generic technology scopes such as `terraform`, `opentofu`,
  `helm`, `k8s`, `kubernetes`, `yaml`, `go`, `python`, `shell`.

When the technology IS the area being changed, technology scopes are
fine:

- `chore(helm): update chart testing config`
- `fix(k8s): correct shared namespace labels`
- `chore(terraform): update provider constraints`

When the change is about a specific module/chart/service, scope by
that thing, not by the underlying technology:

- For Terraform/OpenTofu: prefer module / resource area / product /
  environment / directory name.
  - `feat(gitlab-access-token): add project access token resource`
  - NOT `feat(terraform): add project access token resource`
- For Helm: prefer the chart / application / service / component name.
  - `fix(prometheus): update external secret template`
  - NOT `fix(helm): update external secret template`
- For Kubernetes manifests: prefer the app / namespace / controller /
  operator / workload / platform area.
  - `feat(opensearch): add event log index template`
  - `fix(ingress): correct external traffic policy`
  - NOT `feat(k8s): add event log index template`
- For CI/CD: prefer the pipeline / job / tool / repo / affected project.
  - `ci(gitlab-access-token): add terraform validation job`
  - `ci(renovate): add module update rules`

If several files use the same technology but belong to one
feature/module, scope by that feature/module. If a change spans
multiple unrelated areas, use a broader repository / platform / domain
scope, or omit the scope only when no clear scope exists.

Scope must be short, lowercase, and kebab-case when it contains
multiple words. Examples: `gitlab-access-token`, `prometheus`,
`opensearch`, `ingress`, `k8s-cluster`, `provider`, `backend`, `deps`,
`ansible`, `brew`.

## Subject line

- Imperative mood: `add`, `update`, `switch`, `allow`, `make`,
  `replace`, `harden`, `templatize`, `remove`, `drop`, `rename`.
- Do not capitalize the first word after the colon.
- Do not end the subject with a period.
- Keep it concise and specific. Max 72 characters.

## Dependency updates

Use the shorthand:

```
chore(deps): update <dependency> to <version>
```

## Authorship trailers

Never add AI attribution to a message you propose. No
`Co-Authored-By:` for Claude, Codex, OpenAI, Anthropic, or any other
model or assistant; no `Generated with Claude Code`; no
`Claude-Session:` trailer. The commit is the user's.

Add such a trailer only when the user asks for it explicitly in this
invocation - a hint like "credit Claude as co-author" counts, the
absence of any instruction does not.

Trailers the user already wrote are not yours to remove: when you
reword or squash existing commits, carry over their `Co-Authored-By:`,
`Signed-off-by:`, `Refs:`, and similar trailers unchanged.

## Scope-selection cheatsheet

When you are about to write a scope and find yourself reaching for
`terraform`, `helm`, `k8s`, `kubernetes`, `yaml`, `go`, `python`, or
`shell`: pause. Ask "what is this change ABOUT?" The answer is the
scope - the technology is just the tool the change happens to use.

