---
title: "Skill Marketplace"
description: "Publish approved skills from one Git repository to the Agents of policy-bound Teams."
weight: 27
---

The Skill Marketplace delivers approved skills from one Git repository to the
Agents of policy-bound Teams. An administrator selects the skills of each
Team. The OpenClaw and Hermes Agents of the Team receive these skills
read-only in `$HOME/.aks/skills`, and its Custom Agents in
`$HOME/.agents/skills`.

The Skill Marketplace is optional, and it is off by default. The operator runs
it in two parts:

- The operator lists the Teams with an Agent Policy and their selected skills
  for the worker. It also gives each Agent of these Teams a read-only claim of
  a Blob NFS container. When the Agent starts, its HOME initializer copies the
  current release of its Team. The copy is the skills view of the Agent.
- The worker is the Deployment `cetirun-skill-marketplace` in the operator
  namespace. It runs the operator image in a Pod of its own. It does these
  tasks:

  1. At a fixed interval, it examines the configured branch or tag of the
     repository. When it finds a new commit, it reads that commit into memory.
     It does not check out files, run hooks, or follow submodules. It never
     runs repository content.
  2. It validates each skill of the commit. It lists the valid skills and the
     rejected directories in a catalog.
  3. For each Team that the operator lists, it publishes the selected skills.
     The result is an immutable release in the Blob NFS container.

The worker runs apart from the operator because it reads repository content,
holds the repository credentials, and writes to the container. Refer to
[Security](#security).

Approval occurs in the repository. The worker publishes the content of the
configured branch or tag. Review the changes to that branch or tag as you
review code that your Agents run.

The steps on this page are for an administrator of an installation that
`make deploy` made, as in [Install CetiRun](../../getting-started/platform/).
The installation must run [policy-bound Teams](../policy-teams/). Do these
steps:

1. [Prepare the storage](#storage).
2. [Prepare the repository and its credentials](#the-repository-and-its-credentials).
3. [Enable the Skill Marketplace](#enable-the-skill-marketplace).
4. [Select the skills of a Team](#select-the-skills-of-a-team).

## Skills in the repository

A skill is a directory below `repository.path` that contains a `SKILL.md`
file. The name of the directory is the name of the skill. `SKILL.md` starts
with YAML frontmatter that gives this name and a description:

```markdown
---
name: pdf-tools
description: Extract text and tables from PDF files.
---

Read the PDF with the scripts in this directory.
```

The worker rejects a skill directory in these cases. The catalog gives the
reason for each rejected directory.

- `SKILL.md` does not start with frontmatter between `---` lines, or the
  frontmatter is not valid YAML.
- The frontmatter has no `name`, or the `name` is not the name of the
  directory.
- The frontmatter has no `description`. Or the description is longer than the
  description limit, or it has control characters other than line breaks and
  tabs.
- The name is not words of lowercase letters and digits that single hyphens
  join. Or the name has more than 64 characters. For example, `pdf-tools` is a
  valid name, and `PDF_Tools` is not.
- The name is reserved. By default, `cetirun-memory` and `connected-services`
  are reserved, because CetiRun and its Agent images supply skills with these
  names.
- The directory contains a symbolic link or a Git submodule. This includes a
  `SKILL.md` that is a symbolic link to another skill's file.
- A file or directory name in the skill has a control character or a
  backslash. A name that Git uses for its own directory, such as `.git`, is
  also not allowed. Such names in other parts of the repository do not stop
  the synchronization.
- A file or directory name in the skill has more than 255 bytes, for example
  86 Chinese characters. The container and the file systems of the Agents
  cannot store such a name.
- The skill has more files, bytes, or directory levels than the
  [skill limits](#skill-limits) allow. By default, a skill can have at most
  500 files and 5 MiB in total. Its `SKILL.md` can have at most 256 KiB. No
  file can be more than 8 levels below the skill directory.
- Another skill directory has the same name. In this case, the worker rejects
  all the directories with that name.

These rules also apply:

- Put each skill in its own directory. The worker rejects a `SKILL.md`
  directly in `repository.path`.
- A `SKILL.md` in a subdirectory of a skill does not make a second skill. The
  subdirectory is part of the outer skill, and the catalog lists it as
  rejected.
- The worker ignores a `SKILL.md` in a directory whose name starts with `.`,
  for example `.github`. The same applies to the directories below it.
- The worker refuses a commit that exceeds the repository limits: by default,
  256 MiB and 100,000 objects. Files outside `repository.path` count too,
  because the worker reads the whole commit.

The version of a skill is the first 12 hexadecimal characters of the Git tree
hash of its directory. When a file of the skill changes, its version changes.
Some pages of the Portal show only the first seven characters.

### Skill limits

The limits are values of the operator chart, below `skillMarketplace.limits`. The name rule,
at most 64 characters, and the default description limit, 1,024 characters,
come from the [Agent Skills specification](https://agentskills.io/specification).
The other limits are defaults that CetiRun chose to keep each skill small.

| Value | Default | Limit |
| --- | --- | --- |
| `limits.maxRepositorySize` | `256Mi` | Size of the repository commit: the pack that the Git server sends for it, its objects together, and the paths of its files and directories below `repository.path` together |
| `limits.maxRepositoryObjects` | `100000` | Objects of the repository commit: its files, its directories, and the commit. Below `repository.path`, each path counts |
| `limits.maxSkillFiles` | `500` | Number of files in one skill |
| `limits.maxSkillSize` | `5Mi` | Total size of the files of one skill |
| `limits.maxSkillMDSize` | `256Ki` | Size of `SKILL.md`. It cannot be larger than `maxSkillSize`. |
| `limits.maxSkillDepth` | `8` | Directory levels of a file below the skill directory |
| `limits.maxDescriptionLength` | `1024` | Characters of the `description` |

The worker holds the commit in memory. It checks the repository limits before
it decodes a commit. It stops the transfer when the pack from the Git server
exceeds `maxRepositorySize`. Then it reads the object headers of the pack
before it decodes any object. A commit that exceeds a limit does not reach
memory.

Until the repository has a commit within the limits, the catalog keeps the
last commit that the worker read. The worker reads a commit that exceeds a
limit once. It examines the repository again at each check, but it reads that
commit again only after a restart, for example with other limits.

Git stores a directory once, also when the commit holds it at many paths. When
the worker reads the tree below `repository.path`, it counts each path. A
directory that the commit holds at two paths counts twice, with all the files
and directories in it. The worker stops at the first path over
`maxRepositoryObjects`, or when the paths together exceed `maxRepositorySize`.

You cannot change the 64-character name limit, because Team and Policy
validation also use it. Before you increase a size limit, do these checks:

- When an Agent starts, it copies all the skills of its Team into the scratch
  space of its Sandbox. Make sure that the copy fits in the scratch size of
  the Sandboxes of the Team.
- Keep the memory limit of the worker, `skillMarketplace.worker.resources.limits.memory`, at least
  three times `maxRepositorySize` plus 256 MiB. A commit needs memory for its
  objects, and once more for its largest object or for the paths below
  `repository.path`. While the worker reads a new commit, it still holds the
  files of the valid skills of the commit that it read before. The default
  memory limit, `1Gi`, fits the default repository limits. Increase the memory
  limit together with either repository limit. The worker sets the soft
  memory limit of its Go runtime to 90% of the memory limit of its container.
- The worker writes the pack of a check to an `emptyDir` volume of its Pod.
  It removes the pack after the check. The pack is at most
  `maxRepositorySize`.

With `make deploy`, set the values in the environment file below
`platform.values.cetirun-operator.skillMarketplace.limits`. The worker uses
changed limits when it starts again, and the Helm upgrade starts it again.

## Team releases

For each Team with an Agent Policy, the worker writes the selected skills to
the subtree of the Team in the container:

```text
teams/<team-uid>/CURRENT                            ID of the current release
teams/<team-uid>/releases/<id>/<skill>/...          one directory for each skill
teams/<team-uid>/releases/<id>/.cetirun-selection   the selection of the release
teams/<team-uid>/releases/<id>/.cetirun-release.json
```

- A release contains only the skills of the selection of the Team. Entries
  with the `npm:` prefix are not part of it.
- The release ID depends only on the selection and on the versions of its
  skills. When neither changes, the worker does not publish a new release.
- The worker writes a complete release before it replaces `CURRENT`. After
  that, it does not change the release. A release that is needed again, for
  example after a revert, is written again unless it is complete.
- The worker keeps the current release and the two newest other releases. It
  removes an older release when it has not been current for ten minutes, so
  an Agent that started just before the change can still copy it.
- When a Team leaves the list of policy-bound Teams, the worker keeps its
  releases for one hour (`teamGCGrace`) after it finds the Team gone. A Team
  leaves the list when you delete it, or when you remove its Agent Policy. A
  Team that comes back within the hour keeps its releases, and with them its
  retained skills. When the worker starts again, the hour starts again.
- The worker removes releases during a full pass. It makes a full pass after
  each new commit or change of a policy-bound Team, and every ten minutes.
  Thus a removal can occur up to ten minutes after the times above.
- The worker removes a release or the subtree of a Team by renaming it first.
  A removal that stops part way never leaves an incomplete release under its
  own name.

The worker publishes again when the selection of a Team changes, and when it
reads a new commit. When the publication of a Team fails, the worker tries
again after ten seconds. While the same selection and commit keep failing, it
waits twice as long before each new try, up to five minutes. A change of the
selection or of the commit tries again at once. The log of the worker has the
error when the failure starts or its error changes, one short line for each
further attempt, and one line when the publication succeeds again.

## How Agents receive the skills

The operator gives each Agent of a policy-bound Team a claim in the namespace
of the Agent. The claim has the name of the operator's
`sandboxSources.skillsClaimName`, which is `skills` by default. Its
PersistentVolume refers to the container read-only.

When the Agent starts, its HOME initializer mounts only the subtree of the
Team of the Agent, read-only. The initializer waits until `CURRENT` names a
release of the current selection of the Team. Then it copies the skills of
that release into the context view of the Pod. No Agent container mounts the
claim. The operator refuses an Agent chart that mounts the claim in another
container, or that mounts more than the subtree of the Team.

An OpenClaw or Hermes Agent container receives the copy read-only at
`$HOME/.aks/skills`. A Custom Agent container receives the whole context view
read-only at `$HOME/.agents`. There, the skills of the release are in
`$HOME/.agents/skills`, next to the platform skills, such as `cetirun-memory`.

The view is an `emptyDir` volume of the Pod. The scratch size of the Sandbox
of the Agent limits its size. After the start, the Agent reads only its copy.
A release that the worker publishes later reaches the Agent at its next
start.

OpenClaw loads `$HOME/.aks/skills` as an extra skill directory, and Hermes
loads it as an external skill directory. A Custom image loads the skills of
`$HOME/.agents/skills` as the Custom contract requires. Refer to
[Common initialization and discovery](../../reference/agent-harness/#common-initialization-and-discovery).

### Skill sources of an Agent

The Skill Marketplace is one of two sources of Team skills. Each Agent uses
only one of them:

| Agent | Source of the Team skills |
| --- | --- |
| An OpenClaw, Hermes, or Custom Sandbox Agent of a Team with an Agent Policy | The Skill Marketplace. The Agent receives only the skills that its Team selects, from the release that is current when the Agent starts. |
| An Agent of a Team without an Agent Policy, or an Agent without a Team | The shared skills container: `sharedSkills` of `make deploy`, or the container that the `skillStorage` of the Team names. The Agent receives all the skills in the container. |

A Skill Marketplace Agent does not mount the shared skills container. The
other Agents do not receive marketplace skills.

`make deploy` requires the `sharedSkills` container, but it does not add skills
to it. If no Agent uses the container, the container stays empty.

An OpenClaw or Hermes Agent of the Skill Marketplace loads its skills from
these locations:

- `$HOME/.aks/skills`: the Team skills, read-only.
- `$HOME/.agents/skills`: personal skills in HOME. The user and the Agent can
  change them.
- `/opt/cetirun-memory/skills`: the platform skills, such as
  `cetirun-memory`.
- The skills that are part of OpenClaw or Hermes.

A Custom Agent of the Skill Marketplace finds the Team skills and the
platform skills together in `$HOME/.agents/skills`, read-only. The image
decides how it loads them, and where it keeps personal skills.

If two skills have the same name, the runtime selects one of them. Refer to
[Limits](#limits).

## Prerequisites

- **Sandbox execution.** Enable it as
  [Enable Sandbox execution](../policy-teams/#enable-sandbox-execution) shows.
  The Skill Marketplace needs `sandboxSources.enabled` and
  `sandboxSources.nativeExecution`. It replaces the published skills catalog.
  As a result, `sandboxSources.skillsCatalogConfigMapName` must stay empty.
- **Memory.** For OpenClaw and Hermes, the chart composes the skills view
  together with Memory. Keep `cetirun-operator.memoryContext.enabled`, which
  `make deploy` sets. Without Memory, the render of the Agent fails with this
  message: `Skill Marketplace skills require the composed Agent context`. A
  Custom Agent always has its composed context view.
- **`sleep` in Custom images.** The HOME initializer of a Custom Agent runs in
  the image of the Agent. It uses `sleep` while it waits for the release.
  Without `sleep`, it stops with `InitToolsMissing: sleep`.
- **The Blob CSI driver** and one of its StorageClasses, for example
  `azureblob-nfs-premium`.
- **A storage account and an empty container**, as [Storage](#storage) shows.
- **A Git repository** and, for a private repository, its credentials, as
  [The repository and its credentials](#the-repository-and-its-credentials)
  shows.

### Storage

The worker needs a Blob container of its own. The container must be in a
storage account with NFS 3.0, such as the Premium BlockBlobStorage account
that the storage helper creates. Do not use the container of the shared skills
(`sharedSkills`). `make deploy` refuses it. The two containers serve different
Agents. Refer to [Skill sources of an Agent](#skill-sources-of-an-agent).

NFS 3.0 has no client authentication. The storage service authorizes NFS
requests only by the network that they come from. Use a storage account that
only private endpoints can reach, and turn off its public network access. The
[storage helper](../storage/#private-endpoint) creates such an account with
these variables:

```bash
RESOURCE_GROUP=example-rg AKS_CLUSTER_NAME=example-cluster \
PARAM_FILE=/absolute/path/marketplace-storage.bicepparam \
PRIVATE_ENDPOINT_SUBNET_ID="/subscriptions/<subscription-id>/resourceGroups/example-network-rg/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/example-nodes" \
PRIVATE_DNS_ZONE_ID="/subscriptions/<subscription-id>/resourceGroups/example-network-rg/providers/Microsoft.Network/privateDnsZones/privatelink.blob.core.windows.net" \
PUBLIC_NETWORK_ACCESS=Disabled \
  bash infra/storage/deploy.sh
```

In the parameter file, set `location`, `storageAccountName`, and
`containerName`, for example `examplemarketplace` and `marketplace`. The
private DNS zone `privatelink.blob.core.windows.net` must exist, and it must
be linked to the virtual network of the cluster. The nodes then resolve the
account to its private address. The helper does not create the zone or the
link. Refer to [NFS security properties](../storage/#nfs-security-properties).

Use an empty container. At each start, the worker refuses a container that
has entries that it did not write. Only the worker writes to the container.
Do not copy files into it.

### The repository and its credentials

`repository.auth` selects how the worker reads the repository:

| `auth` | URL | Keys of the Secret |
| --- | --- | --- |
| `none` | `https://` | None |
| `token` | `https://` | `token`: a token with read access to the repository |
| `ssh` | `ssh://` or `user@host:path` | `ssh-privatekey`: a key without a passphrase. `known_hosts`: the host keys of the Git host |

Never put credentials in the URL. `make deploy` and the chart refuse a URL
with credentials. The log of the worker and the catalog show the URL without
user information.

With `token`, the worker sends the token as the password of HTTP basic
authentication, with the user name `x-access-token`. For a Git host that
needs another user name, set `repository.tokenUsername` under
`platform.values.cetirun-operator.skillMarketplace`. With `ssh`, the worker refuses a
host whose key is not in `known_hosts`. For an `ssh://` URL without a user, the
user is `git`.

Use a token that can read only the skills repository:

- **GitHub**: a fine-grained personal access token. Select only the skills
  repository, and give it read-only access to `Contents`.
- **Azure Repos**: a personal access token with the `Code (Read)` scope. Such a
  token can read every repository of the organization that its owner can read.
  Use an account that can read only the skills repository.

The worker does not get Microsoft Entra tokens for itself. An organization
that does not allow personal access tokens cannot use `token`.

When the token expires or is revoked, each check fails with
`repository access was refused`. The catalog keeps the last commit, and the
Teams keep their releases. Replace the Secret with a new token.

Create the Secret in the platform namespace. Use `kubectl create`, not
`kubectl apply`. The apply command also copies the credential into the
`last-applied-configuration` annotation. For a token:

```bash
kubectl create secret generic skills-repository -n cetirun-system \
  --from-file=token=./skills-token.txt
```

For a deploy key:

```bash
ssh-keyscan git.example.invalid > known_hosts
ssh-keygen -lf known_hosts
kubectl create secret generic skills-repository -n cetirun-system \
  --from-file=ssh-privatekey=./skills-deploy-key \
  --from-file=known_hosts=./known_hosts
```

Compare the fingerprints that `ssh-keygen -lf` shows with the fingerprints
that your Git host publishes. Then delete the local copy of the token or the
key.

The worker reads the credentials at each check of the repository. When you
replace the Secret, the worker uses the new credentials after the kubelet
updates the mounted files. A restart is not necessary.

## Enable the Skill Marketplace

Add the `skillMarketplace` block at the top level of the environment file of
`make deploy`. Keep the other values, which include the values of Sandbox
execution. You can add the block in the run that enables Sandbox execution, or
in a later run.

```yaml
skillMarketplace:
  repository:
    url: https://git.example.invalid/example/skills.git
    ref: main                  # Optional. Empty uses the remote HEAD.
    path: skills               # Optional. Empty is the repository root.
    auth: token                # none, token, or ssh
    existingSecret: skills-repository
    syncInterval: 5m           # Optional. At least 30s.
  storage:
    resourceGroup: example-rg
    storageAccount: examplemarketplace
    containerName: marketplace
    storageClassName: azureblob-nfs-premium
```

| Field | Necessary | Meaning |
| --- | --- | --- |
| `repository.url` | Yes | The repository, in the form that `auth` needs. It cannot contain whitespace, credentials, a query, or a fragment |
| `repository.ref` | No | A branch, a tag, or a full ref such as `refs/heads/main`. For a name without `refs/`, the worker uses the branch with that name, or else the tag. A tag can be a lightweight or an annotated tag. Empty uses the branch that the remote HEAD names |
| `repository.path` | No | The directory of the skills, relative to the repository root, without `..`. Empty is the root |
| `repository.auth` | Yes | `none`, `token`, or `ssh` |
| `repository.existingSecret` | With `token` or `ssh` | The Secret in `platform.namespace`. Not allowed with `none` |
| `repository.syncInterval` | No | How often the worker examines the remote, as a duration such as `5m` (the default) or `1h`. At least `30s` |
| `storage.resourceGroup` | Yes | The resource group of the storage account |
| `storage.storageAccount` | Yes | The storage account: 3 to 24 lowercase letters and digits |
| `storage.containerName` | Yes | The container of the releases. It cannot be the `sharedSkills` container |
| `storage.storageClassName` | Yes | A StorageClass of the Blob CSI driver |

To move the releases to other storage, change the `storage` fields and run
`make deploy` again. Refer to [Move to other storage](#move-to-other-storage).

`make deploy` does these things with the block:

- It refuses the block when Sandbox execution is off, or when
  `sandboxSources.skillsCatalogConfigMapName` is set.
- Before it writes, it makes sure that the Secret has the keys that `auth`
  needs, and that the StorageClass exists.
- It sets the switches `cetirun-operator.skillMarketplace.enabled` and
  `cetirun-portal.skillMarketplace.enabled`, and the repository and storage
  values of the block below `cetirun-operator.skillMarketplace`. It sets the
  catalog namespace of the Portal to `platform.namespace`. Do not set these
  values under `platform.values`. A different value stops the run. A switch
  without the block also stops it.
- The worker runs the operator image of the `release` section. The claims of
  the Agents have the name of the operator's `sandboxSources.skillsClaimName`.
- It accepts only the fields of the table. Set the other values of the
  marketplace under `platform.values.cetirun-operator.skillMarketplace`, for
  example `repository.tokenUsername` or `worker.resources`. Refer to
  [Skill Marketplace values](../../reference/helm/#skill-marketplace-values).

Run the offline plan first. Then run the deployment:

```bash
make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
```

The run waits until the worker is ready. A ready worker has not necessarily
read the repository. Make sure that the catalog shows a commit and no error:

```bash
kubectl get configmap cetirun-skill-marketplace -n cetirun-system \
  -o jsonpath='{.data.catalog\.json}' | jq '{commit, syncedAt, checkedAt, error}'
```

**Admin → Skills** in the Portal shows the same state.

### Move to other storage

To move the releases to another container or storage account, for example to
one that only private endpoints reach, prepare an empty container as in
[Storage](#storage). Change the `storage` fields in the environment file, and
run `make deploy` again. Then:

- The worker gets a volume and a claim of the new storage. Helm removes its
  earlier volume and claim. The earlier container keeps its data.
- The worker publishes the release of each Team in the new container, from
  the repository. A [retained skill](#retained-and-missing-skills) exists only
  in the earlier container, so it becomes missing. To keep such a skill, add
  it to the repository again before the move.
- The operator replaces the skills claim of each Agent of a policy-bound
  Team. Each running Agent stops and starts again one time, and then copies
  the release of its Team from the new container.

### Turn off the Skill Marketplace

Remove the `skillMarketplace` block from the environment file, and run
`make deploy`. Then:

- Helm removes the worker, and its volume and claim. The container keeps the
  releases, and the Agents keep their skills claims.
- An Agent of a policy-bound Team that selects skills changes to `Failed`
  with `AgentConfigInvalid`. The message names the skills, and says that the
  Skill Marketplace is off. Its running Pod keeps running with the skills
  that it copied. The operator does not apply changes to the Agent until you
  remove the skills from the Team, or turn the Skill Marketplace on again.
- When you add the block again and run `make deploy`, these Agents become
  `Ready` again. They restart only to apply changes that waited, for example
  a new release that a `make deploy` run installed while the Skill
  Marketplace was off.

## Select the skills of a Team

A Team can select a skill only when its Agent Policy allows that skill. Allow
the skills in the Policy first. Then select them on the Team.

### In the Portal

1. Select **Admin → Agent policies**, and open the Policy of the Team. Under
   **Skill Marketplace skills**, select the skills that the Policy allows. Each
   selected skill adds its name to **Allowed skills**. The other entries of
   that list stay, for example `npm:` packages. Save the Policy. This change
   does not restart Agents.
2. Select **Admin → Teams**, and open the Team. Under **Skills**, select the
   skills that the Agents of the Team get. **Search skills** finds a skill by
   its name or its description. A skill that the Policy does not allow shows
   **Not allowed by the Agent Policy**, and you cannot select it. Enter npm
   packages in **npm skill packages, one per line**, each with the `npm:`
   prefix.
3. Save the Team. When its skills change, the save restarts the running
   Agents of the Team one time.

The Portal refuses these changes:

- A Team skill that the Policy does not allow.
- A new Team skill that is not in the catalog. If the worker rejected the
  copy in the repository, the message gives the reason.
- A Team entry without the `npm:` prefix that is not a skill name.
- A Policy save that removes a marketplace skill that a Team of this Policy
  still selects. Remove the skill from the Team first.

While the catalog is not available, the Team page keeps the selected skills,
but you cannot add a skill. A selected skill that the catalog no longer has
shows as **(unavailable)**. You can remove it, or keep it as a
[retained skill](#retained-and-missing-skills).

**Admin → Skills** shows the state of the Skill Marketplace. The Portal shows
this entry only when the Skill Marketplace is enabled. The page has these
sections:

- **Repository**: the URL, the ref, the path, and the synchronized commit. It
  also shows when the worker last synchronized and examined the repository.
- **Skills**: each valid skill, with its directory, description, and version.
  It also shows the time when the worker first saw that version, the number
  of files, and the size.
- **Invalid skills**: each rejected directory, with the reason.
- **Team publications**: each policy-bound Team, with its release, the time of
  publication, and the number of skills. **Details** lists retained and
  missing skills and a publication error. The state of the release is one of
  these:
  - **Up to date**: the release serves the current selection of the Team, with
    the repository's version of each skill.
  - **Update pending**: the release serves the current selection, but not the
    current repository. The repository has another version of one of its
    skills, or it added a missing skill.
  - **Pending**: the worker has not published a release of the current
    selection yet.

**Refresh** reads the catalog again. It does not make the worker read the
repository.

On the page of an Agent, its owner sees the marketplace skills of the Team
under **Team skills**. The version of a skill is the version in the release
of the Team. The Agent copies that release when it starts. Each skill has one
of these states:

- **Published**: the release has the repository's version of the skill.
- **Update pending**: the repository has another version, which the release
  does not have yet. The section shows it as the repository version.
- **Pending**: the worker has not published a release of the current
  selection of the Team yet. The next start of the Agent waits for it.
- **Retained** or **Missing**: refer to
  [Retained and missing skills](#retained-and-missing-skills).

When the worker could not publish the skills of the Team, the section says
so. The **Skills** page shows the error. When the Team has a newer release
than the Agent copied, the section says that the Agent uses the earlier
versions until it restarts. Refer to
[When changes take effect](#when-changes-take-effect).

### With kubectl

This is the same configuration as resources. Apply it as an administrator that
`sandboxAdmission` names, as
[the admission certificate](../policy-teams/#the-admission-certificate) shows:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: AgentPolicy
metadata:
  name: research
spec:
  harnesses:
    allowed: [openclaw, hermes]
  skills:
    allowed:
      - pdf-tools
      - web-research
      - npm:@example/report-skill
  modelAccess:
    sharedModels:
      allowed: [gpt-main]
    gatewayBudget:
      defaultMonthlyUSD: "10"
      maxMonthlyUSD: "20"
  security:
    readOnlyRootFilesystem: true
  network:
    ingress:
      mode: authenticated
---
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: research
spec:
  agentPolicyRef: research
  sandboxRefs: [standard]
  defaultSandboxRef: standard
  skills:
    - pdf-tools
    - web-research
    - npm:@example/report-skill
```

- Each entry without the `npm:` prefix names a marketplace skill. The order of
  the entries does not matter. A Team has at most 100 entries, npm packages
  included.
- The checks of the Portal do not apply to `kubectl`. The worker lists a
  selected name that the catalog does not have as
  [missing](#retained-and-missing-skills).
- Keep each Team skill in the allowed skills of the Policy. The Sandbox
  admission webhook refuses a Team that selects a skill that its Policy does
  not allow, also for a change of the skills alone. It also refuses a Policy
  change that removes a skill that a Team of the Policy selects. The message
  names the Team and the skills.
- A Team that was stored before the webhook checked its skills can still
  select such a skill. The operator then denies the Agents of the Team. The
  condition `PolicyResolved` of these Agents is `False`, with reason
  `PolicyDenied` and the message `skill "<name>" is not allowed by policy`.
  The operator revokes their Gateway keys and stops them.

## When changes take effect

| Change | Effect |
| --- | --- |
| The skills of a Team, in the Portal or with `kubectl` | The worker publishes a release for the new selection when it sees the change. The change restarts the running Sandbox Agents of the Team one time, as each Team change does. Each new Pod waits for the release of the new selection, and then copies it |
| A new commit on the configured branch or tag | At its next check, the worker publishes a new release for each Team whose selected skills changed. Running Agents keep their copy. Each Agent receives the new versions at its next start |
| The allowed skills of a Policy | No restart. The allowed skills only limit what Teams and Agents can select |

To give a running Agent the newest versions now, restart it. On the page of
the Agent, its owner selects **Restart Agent**. Refer to
[Restart while retaining HOME](../../guides/lifecycle/#restart-while-retaining-home).
An Agent also receives the newest versions when its Pod starts again for
another reason.

The Portal shows when a restart is necessary. When the HOME initializer of
the Agent completes, it writes the ID of the release that it copied into its
termination message, `cetirun.skills.release=<id>`. When the current release
of the Team has another ID for the same selection, **Team skills** on the page
of the Agent shows a warning. If the owner can restart the Agent, the warning
has a **Restart Agent** button. An Agent with an earlier Agent chart writes no
ID, so the Portal shows no warning for it. To read the ID with `kubectl`:

```bash
kubectl get pods -n cetirun-<agent> -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.initContainerStatuses[?(@.name=="init-home-data")].state.terminated.message}{end}'
```

The worker examines the repository when it starts, and then at each
`syncInterval`. After a failed check, it tries again after one minute, or
after `syncInterval` when that is shorter. To make the worker examine the
repository before the next interval, restart it:

```bash
kubectl rollout restart deployment/cetirun-skill-marketplace -n cetirun-system
```

A restarted worker keeps the catalog of the earlier one until it knows more:
the synchronized commit with its skills, and each Team publication. On the
first start of the worker, the catalog shows no skills until the first
successful check. During that time, the Portal cannot add skills to Teams.

If the worker cannot publish the release of a new selection, the restarted
Agents do not complete their start. Refer to
[A Team release is pending](#a-team-release-is-pending).

## Retained and missing skills

A selected skill can stop being valid in the repository. For example, someone
removes or renames its directory, or a change breaks a validation rule. The
next release of the Team then keeps the version of the skill that the current
release of the Team has. The catalog lists the skill under `retained` for the
Team, and the Portal shows **Retained**. The skill stays retained until it is
valid again, or until you remove it from the Team.

The worker copies a retained skill from the current release of the Team. If
it cannot read that release or the skill in it, it does not publish a release
without the skill. It keeps the current release, it reports the error for the
Team, and it tries again. Refer to
[A Team release is pending](#a-team-release-is-pending).

A selected skill is missing when it is not valid in the repository and not in
the current release of the Team. The worker
publishes the release without it. The catalog lists the skill under `missing`,
and the Portal shows **Missing**. The Portal does not add such a skill to a
Team, but `kubectl` can.

Neither state stops the Agents of the Team. To clear a retained or a missing
skill, remove it from the Team. Then you can remove it from the Policy.

## Limits

- **One repository.** All Teams of the installation select from one
  repository, branch or tag, and path.
- **Names, not versions.** A Team selects skills by name. Its release has the
  version of the last synchronized commit, or the retained version.
- **Sandbox Agents of policy-bound Teams only.** The Skill Marketplace
  delivers skills to OpenClaw, Hermes, and Custom Agents that the common
  executor runs. These are the Sandbox Agents that are created after you
  enable Sandbox execution. These Agents do not receive marketplace skills:
  - Agents with a retained version-1 native single-start lifetime.
  - Agents of a Team without an Agent Policy, and Agents without a Team. For
    these Agents, the operator does not install entries without the `npm:`
    prefix.
- **Agent chart version.** An Agent keeps the Agent chart version that it was
  created with (`spec.helm.version`). These charts cannot give an Agent the
  skills of its Team:
  - A chart from a release before the Skill Marketplace.
  - For a Custom Agent, a chart without the annotation
    `cetirun.microsoft.com/custom-skill-marketplace: v1`.

  While the Team of such an Agent selects marketplace skills, the operator
  sets the Agent to `Failed` with `AgentConfigInvalid`. Upgrade its chart, as
  [Pin and update a chart](../../guides/lifecycle/#pin-and-update-a-chart)
  shows.
- **No change for each Agent.** `skillOverrides` cannot add or remove a
  marketplace skill. While the Skill Marketplace is on, the operator refuses
  such an Agent, with or without a policy. The Agent changes to `Failed` with
  `AgentConfigInvalid` and this message:
  `skillOverrides cannot add or remove Skill Marketplace skills; select them on the Team`.
  `npm:` entries in `skillOverrides` stay possible.
- **Name conflicts with private skills.** CetiRun does not compare marketplace
  names with the skills in the HOME of an Agent. The runtime decides which of
  two skills with the same name it uses. For OpenClaw and Hermes, a private
  skill, for example in `$HOME/.agents/skills`, can hide the marketplace skill.
- **100 entries.** The `skills` of a Team have at most 100 entries, npm
  packages included.
- **Read-only.** Agents cannot change marketplace skills. Make changes in the
  repository.
- **Scratch space.** The copy of the skills of the Team must fit in the
  scratch size of the Sandbox of the Agent.

## Security

- The worker runs one replica as a non-root user, with a read-only root
  filesystem and without an Istio sidecar. It uses only the Kubernetes API,
  the Git host, and its NFS mount.
- The worker has no cluster-wide permissions. In the operator namespace, its
  Role can read the ConfigMap `cetirun-skill-marketplace-teams`, which the
  operator writes. It can also read and update the catalog ConfigMap
  `cetirun-skill-marketplace`, which the operator creates. It cannot read
  Teams, Agents, Secrets, claims, or volumes.
- The operator creates the claims and their PersistentVolumes, with its own
  permissions. It creates claims only in the protected namespaces of Agents,
  and volumes only for the configured container. It never updates them. It
  replaces a claim whose volume refers to other storage than the configured
  one, and a volume that refers to an earlier claim of the Agent.
- While the Skill Marketplace is on, the operator deletes the
  PersistentVolume of an Agent whose claim is gone. A claim is gone, for
  example, after the deletion of its Agent. The data in the container stays.
- Agents can only read the skills, and only the HOME initializer mounts the
  claim. Skills can contain scripts that the Agents run. Review the repository
  as you review code.

## Troubleshooting

### Read the catalog

The catalog is the key `catalog.json` of the ConfigMap
`cetirun-skill-marketplace` in the operator namespace. The Portal reads the
same document. It contains no credentials.

```bash
kubectl get configmap cetirun-skill-marketplace -n cetirun-system \
  -o jsonpath='{.data.catalog\.json}' > catalog.json
jq '{repository, commit, syncedAt, checkedAt, error, truncated}' catalog.json
jq '.invalid' catalog.json
jq '.teams[] | {name, release, selectionDigest, releaseSelectionDigest, retained, missing, error}' catalog.json
```

| Field | Meaning |
| --- | --- |
| `repository` | The URL without user information, the ref, and the path |
| `commit`, `syncedAt` | The last commit that the worker read, and when |
| `checkedAt` | When the worker last examined the remote |
| `error` | The error of the last check of the repository. The catalog keeps the skills of the last commit that the worker read. The error of a publication is the `error` of its Team |
| `skills` | The valid skills, with `name`, `description`, `version`, `path`, `updatedAt` (when the worker first saw this version), `files`, and `bytes` |
| `invalid` | The rejected directories, with `path`, `name`, and `reason` |
| `teams` | Each policy-bound Team, with `release`, `skills`, `retained`, `missing`, `error`, and two selection digests. The release serves the current selection of the Team when `releaseSelectionDigest` equals `selectionDigest` |
| `truncated` | `true` when the worker shortened the document to fit the ConfigMap. It shortens long descriptions to 160 characters, and then the list of rejected directories |

### The repository is not synchronized

When a check fails, `error` in the catalog gives the reason. The log of the
worker has the same message when the failure starts or its message changes.
While the same failure repeats, the log has one short line for each check.
When the checks succeed again, the log says so:

```bash
kubectl logs -n cetirun-system deployment/cetirun-skill-marketplace --tail=50
```

| Message | Action |
| --- | --- |
| `repository access was refused` | Examine the token or the deploy key, and its access to the repository |
| `the ref "<ref>" is not in the repository` | Correct `repository.ref` |
| `the repository has no HEAD` or `the repository HEAD is not a branch` | Set `repository.ref` |
| `the repository path "<path>" does not exist in the commit` | Correct `repository.path` |
| `read known_hosts (required for SSH)`, or a host key error | Add the host keys of the Git host to `known_hosts` |
| `parse the SSH private key` | Use a private key without a passphrase |
| `the repository commit exceeds the limits of the Skill Marketplace` | The message names the limit. Make the commit smaller, or increase `limits.maxRepositorySize` (256 MiB by default) or `limits.maxRepositoryObjects` (100,000 by default). Files outside `repository.path` count too. Below `repository.path`, each path counts, also for a directory that the commit holds at several paths. Refer to [Skill limits](#skill-limits). |

### A Team release is pending

The Portal shows **Pending** or **Update pending** for the Team, or
`releaseSelectionDigest` is not equal to `selectionDigest`. Both states are
normal for a short time after a change. If one stays, examine the `error` of
the Team in the catalog:

- `the skills repository has not been synchronized yet`: the worker has not
  read the repository since it started. It keeps the current release of the
  Team, and it tries again every ten seconds. Correct the error of the check.
  After a restart, a Team keeps its earlier publication in the catalog. This
  applies while its selection and its release stay the same.
- `publication waits for the current release, which could not be read`: the
  Team selects a [retained skill](#retained-and-missing-skills). The worker
  cannot read the current release of the Team or the skill in it. It keeps the
  current release, and it tries again. Examine the mount of the worker and
  the container.
- `publication failed`: the worker could not write the release. The message
  gives the reason. When the worker cannot write to the container, examine
  the private endpoint, the DNS zone, and the mount events of the Pod of the
  worker. The worker tries again, with a growing wait while the same error
  repeats, up to five minutes. A change of the selection or a new commit
  tries again at once.

### An Agent waits for its skills claim

A new Agent waits until its namespace has the skills claim. Until then, the
operator does not install the Agent. It reports the wait on the Agent and
tries again every five seconds. The wait does not stop or hold the Agent:

```bash
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.conditions}'
```

`BuiltInPrerequisitesReady` is `False`, with reason
`SkillsMaterializationPending` and this message:
`Waiting for the Skill Marketplace to provide the Team skills claim`. `Ready`
shows the same reason, unless a running workload already made the Agent ready.

The operator creates the claim when the Team of the Agent has an Agent
Policy. The namespace of the Agent must also be protected and belong to the
Agent. The bootstrap of the operator creates such namespaces. If the wait does
not end, examine the operator log and the claim:

```bash
kubectl logs -n cetirun-system deployment/cetirun-operator --tail=200 | grep -i "skills claim"
kubectl get pvc skills -n cetirun-alice-example
kubectl get pv -l skills.cetirun.microsoft.com/marketplace=true
```

The namespace can already have a claim with this name that the operator did
not create for the Skill Marketplace. The operator does not change that claim,
and its log says so once.

When the claim of an Agent is deleted, the operator creates a new claim and
volume. A running Agent keeps its claim until the operator stops the Agent,
which then starts with the new claim. The operator examines a claim that is
not bound yet every ten seconds.

### The initializer of an Agent fails

The HOME initializer of the Pod of the Agent, `init-home-data`, writes its
errors to its log. After a failure, the Pod runs the initializer again. Read
the log:

```bash
kubectl get pods -n cetirun-alice-example
kubectl logs -n cetirun-alice-example openclaw-alice-example-cetirun-0 -c init-home-data
```

Add `--previous` to read the attempt before the current one.

| Error | Cause and action |
| --- | --- |
| `SkillsReleasePending` | For three minutes, `CURRENT` did not name a release of the current selection of the Team. Refer to [A Team release is pending](#a-team-release-is-pending) |
| `SkillsReleaseUnstable` | The current release of the Team changed five times while the initializer copied it. This can occur when the repository and the Team change often. The Pod runs the initializer again |
| `UnsupportedSkillFile` | The release contains a symbolic link or a special file. The worker writes only directories and regular files, so another writer changed the container. Remove the write access of that writer |
| `SharedSkillConflictsWithCanonicalContext` | A skill of the release has the name `cetirun-memory`. Keep this name in `reservedNames` |
| `SharedSkillsUnavailable` | The initializer cannot read the subtree of the Team. Examine the claim, its PersistentVolume, and the mount events of the Pod |
| `InitToolsMissing: sleep` | The image of a Custom Agent has no `sleep`. Add `sleep` to the image |

### The worker does not start

Read the log of the worker. These are frequent causes:

- `which the marketplace did not write; use an empty container`: the
  container has an entry that the worker did not write. Use an empty
  container. The worker writes the marker file `.cetirun-marketplace` at the
  root of the container. A write of the marker can stop before it completes.
  The worker then removes the temporary file of that write at its next start.
- `invalid configuration`: a value is not valid, for example a `syncInterval`
  shorter than `30s`. Correct the value.
- The Pod stays in `ContainerCreating`: the node cannot mount the container.
  Examine the events of the Pod, the private endpoint, the private DNS zone and
  its link, and the StorageClass.

```bash
kubectl describe pods -n cetirun-system -l app.kubernetes.io/name=cetirun-skill-marketplace
```

Sources: [worker]({{< repo "app/cetirun-operator/cmd/skill-marketplace/main.go" >}}),
[skill validation]({{< repo "app/cetirun-operator/internal/skillmarketplace/skills/skills.go" >}}),
[Team releases]({{< repo "app/cetirun-operator/internal/skillmarketplace/publish/publish.go" >}}),
[Teams document]({{< repo "app/cetirun-operator/internal/skillmarketplace/teams.go" >}}),
[Agent claims]({{< repo "app/cetirun-operator/internal/skillmarketplace/claims.go" >}}),
[HOME initializer]({{< repo "app/cetirun-chart/configs/init-home-common.sh" >}}),
[claim wait]({{< repo "app/cetirun-operator/internal/controller/skill_marketplace_claim.go" >}}),
[deployment helper]({{< repo "scripts/cetirun_platform_deploy.py" >}}),
and the platform chart README's
[Skill Marketplace]({{< repo "app/cetirun-platform/README.md" >}}#skill-marketplace).
