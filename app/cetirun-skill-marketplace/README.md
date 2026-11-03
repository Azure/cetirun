# CetiRun Skill Marketplace

`cetirun-skill-marketplace` delivers approved skills from one Git repository
to the Agents of policy-bound Teams. It does these things:

1. It reads one commit of the configured repository into memory. It does not
   check out files to disk, run hooks or follow submodules, and it never runs
   repository content.
2. It finds the skills of that commit. A skill is a directory below
   `repository.path` with a `SKILL.md` file. The YAML frontmatter of that file
   must have a `name`, equal to the directory name, and a `description`.
3. It publishes the skills that each policy-bound Team selects in
   `Team.spec.skills`. The result is an immutable release in the Team's
   subtree of a Blob NFS container. Then it makes that release current.
4. It creates a read-only claim of the container in each Agent namespace of
   these Teams. The Agent's initializer mounts only the Team's subtree. It
   waits for the release of the Team's current selection, and copies it into
   the Agent's read-only `~/.agents/skills` view.
5. It writes the catalog ConfigMap `cetirun-skill-marketplace` in the operator
   namespace. The Portal reads it for the skill pickers and the status page.

A change in the repository takes effect when an Agent starts. A change to a
Team's selection restarts the Team's running Agents once, as any Team change
does.

## Validation

The service rejects a skill directory, and lists the reason in the catalog, in
these cases:

- The directory has no valid frontmatter, or its `name` is not the directory
  name.
- The name is not words of lowercase letters and digits that single hyphens
  join. Or the name has more than 64 characters.
- The name is reserved (`cetirun-memory`, `connected-services`).
- The directory contains a symbolic link or a submodule.
- A file or directory name has a control character or a backslash. Or it is
  a name that Git uses for its own directory, such as `.git`.
- The directory has more than 500 files or more than 5 MiB. Or its `SKILL.md`
  is larger than 256 KiB, or it has files nested deeper than 8 levels.
- Another skill directory has the same name. Both are rejected.

A `SKILL.md` inside a skill directory does not create another skill.

## Storage layout

```text
teams/<team-uid>/CURRENT                          release ID
teams/<team-uid>/releases/<id>/<skill>/...        immutable release
teams/<team-uid>/releases/<id>/.cetirun-selection selection digest
teams/<team-uid>/releases/<id>/.cetirun-release.json
```

The release ID depends only on the Team's selection and the skill versions.
The service keeps the current release and the two newest others, and removes
older releases after ten minutes. A selected skill can stop being valid in the
repository. Then the next release keeps its last published version, and the
catalog lists it as retained.

## Configuration

| Value | Purpose |
| --- | --- |
| `repository.url`, `ref`, `path` | The repository, the branch or tag (empty is the remote HEAD), and the directory of the skills |
| `repository.auth`, `existingSecret` | `none`, `token` (Secret key `token`) or `ssh` (keys `ssh-privatekey` and `known_hosts`) |
| `repository.syncInterval` | How often the remote is checked |
| `storage.*` | The Blob NFS container of the releases |
| `agentClaims.claimName` | Must equal the operator's `sandboxSources.skillsClaimName` |
| `catalog.namespace` | The operator namespace; empty uses the release namespace |

Never put credentials in the URL. The chart refuses an `https://` URL with
user information.

## Security

NFS 3.0 authorizes requests only by network. Use a storage account that only
private endpoints can reach, and turn off its public network access. Agent
Pods cannot reach private addresses, while the node's CSI driver mounts the
container. The service account can create claims and volumes in any namespace,
but only for the configured container, and it never updates them.

Approval happens in the repository: what is on the configured ref is
published. Review changes to that ref as you would review code that your
Agents run.
