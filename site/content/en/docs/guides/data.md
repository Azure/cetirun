---
title: "Protect your data"
description: "Find where your Agent keeps files, what each lifecycle action keeps or removes, how to keep a copy of your work, and who can see your data."
weight: 70
---

Your Agent keeps its files in a persistent home directory. Other data, for
example the GitHub Copilot sign-in and the Chat history, is in other places.
This page shows where your data is, what each action does to it, and who can
see it.

## Where your data is

| Data | Where it is |
| --- | --- |
| Your files, the configuration and the sessions of Hermes or OpenClaw, and the tools that you install in your home | The home directory `/home/vibe` of a Hermes or OpenClaw Agent. A Custom Agent uses `/home/<username>`. The home directory is on a persistent volume. For its size, refer to [Default limits](../troubleshooting/#default-limits). |
| Memory | `~/.cetirun/memory` in the home directory, so it follows the home directory in the table below. Refer to [Use memory](../memory/). |
| Files outside the home directory | The container of the Agent, for example `/tmp`, and software that you install in system directories. These files are lost when the platform replaces the container, for example at a restart. |
| GitHub Copilot sign-in | A secret of the platform, not your home directory. |
| Accounts that you connected | The credential service of the platform. **Connections** lists them. |
| Portal Chat history | Your browser. Refer to [Where history lives](../custom-chat/#where-history-lives). |

## What each action keeps

| Action | Home directory | Files outside home | GitHub Copilot sign-in | Agent Identity | Connected accounts |
| --- | --- | --- | --- | --- | --- |
| **Restart Agent**, **Upgrade to Latest**, and each saved change in **Edit Agent** | Kept | Removed | Kept | Kept | Kept |
| **Suspend Agent** and **Resume Agent** | Kept | Removed | Removed. Sign in again after you resume. | Kept | Kept |
| **Delete Agent** | Deleted. There is no recycle bin. | Deleted | Deleted | Deleted. Refer to [What Agent Identity means for you](../agent-identity/#what-agent-identity-means-for-you). | The access of this Agent is removed. Your personal accounts stay in **Connections**. |

## Keep a copy of your files

The portal has no button to download files. To keep a copy of your work, do
one of these steps in **Terminal**:

- Push your work to a Git repository that you own.
- Copy your files to storage that you control.

Do not keep the only copy of important work in the Agent. Ask your
administrator if the platform also makes backups of the home directories.

## Install software that stays

Software that you install in a system directory, for example with `sudo`, is
lost when the container is replaced. To keep a tool after a restart, install
it in your home directory. In the Sandbox of a policy-bound Team,
`sudo` does not work, so install tools only in your home directory.

## Who can see your data

> **Note:** Your administrators can read the content of your conversations.
> Do not put secrets or the personal data of other people into a conversation.

- **Model requests.** By default, the AI Gateway keeps the request and the
  response of each model call in its database for seven days. People who can
  use the AI Gateway console or read its database can read them. A deployment
  can keep this content in Azure Data Explorer instead. Then the console shows
  it to administrators, and people with read access to that database can read
  it.
- **Logs.** Agents can write text to the container logs. If an administrator
  enables the session logger, it also copies the Hermes gateway log there. The
  platform logs can collect this text, and it can include parts of your
  conversations. People with access to these logs can read it.
- **Files.** Administrators of the cluster can read the files of your Agent.
- **Other users.** Other users cannot open your Agent in the portal. The
  portal checks that you own the Agent.

Your deployment can change these defaults. For the details, refer to
[PRIVACY.md]({{< repo "PRIVACY.md" >}}) and [Security](../../security/), or ask
your administrator.

## Protect your credentials

- Keep the **Gateway Token** and the **Hermes API / Webapp Key** private. They
  open the Agent UI.
- Do not put passwords, keys, or tokens into a conversation, a memory note, or
  a skill.
- Use **Identities** to give the Agent access to external services. With
  **Identities**, the Agent gets only the services that you allow, and the
  platform records each use.

You can sign in to a command-line tool in **Terminal**, for example with
`gh auth login` or `az login`. The tool then saves your credentials in the home
directory. The Agent and all its tools can then use your account with all its
permissions. **Identities** does not control or record this use. When you do
not need the tool, remove its sign-in, for example with `gh auth logout` or
`az logout`.

Sources: [runtime chart values]({{< repo "app/cetirun-chart/values.yaml" >}}),
[AI Gateway chart values]({{< repo "app/cetirun-ai-gateway/chart/values.yaml" >}}),
and [suspension behavior](../../operations/agent-lifecycle/#suspend-and-resume-an-agent).
