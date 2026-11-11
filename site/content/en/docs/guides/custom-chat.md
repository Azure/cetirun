---
title: "Chat with a Custom Agent"
description: "Chat in the portal with a Custom Agent that has no Web UI, and keep the conversation history in your browser."
weight: 45
---

Portal Chat lets you chat with a Custom Agent in the portal. The **Chat** entry
shows in the sidebar of the Agent when these conditions are true:

- The Agent is a Custom Agent with an HTTP port.
- The Agent does not serve a Web UI.
- Your deployment allows Portal Chat.

Hermes and OpenClaw Agents use their own interfaces, which you open with
**Agent UI**. A Custom Agent with a Web UI uses **Web UI**. To enable Portal
Chat for a deployment, administrators refer to
[Enable Portal Chat](../../operations/portal-chat/).

## Start a conversation

1. Open the Agent, and select **Chat**.
2. In the model list, select a model that the Agent offers.
3. Enter a message.
4. Select the send button (arrow), or press Ctrl+Enter (Command+Enter on
   macOS).

The Enter key adds a new line. Text that you type with an input method does
not send the message. While the browser saves the history, you cannot send a
message or select another conversation.

The reply shows while the Agent writes it. To stop the reply, select the stop
button. The Agent gets the cancellation, but a tool that already started can
finish, and its effects stay. If you leave the Chat page, you also stop the
reception of the reply.

On the Chat page, you can create, select, rename, and delete conversations,
and copy text. A new conversation does not start a new Agent. The same Agent
keeps its files, its tools, and its private memory.

If the model list is empty, the Agent has no models yet. Complete the model
setup of the image. For an image that uses the AI Gateway, sign in to GitHub
Copilot on the **Overview** tab, or ask your administrator to share a model.
Chat does not ask for model credentials, and it does not send them to the
browser.

When a reply fails or stops, the conversation shows the actual state of the
reply. A page refresh does not send the request again. Before you send the
next message, examine a reply with an uncertain result.

## Where history lives

The Chat history and your drafts are in your browser, not in the portal or in
the Agent. The portal sends the earlier messages of the conversation with each
request, so the Agent gets the context of the conversation.

- The history is kept for each account and for each Agent. A new Agent with
  the same name does not get the history of a deleted Agent.
- **Local history** in the portal header shows the conversations that this
  browser keeps. It also shows the conversations of Agents that you deleted.
  You can read these conversations, but you cannot send new messages to them.
- The history stays after you sign out. In the **Sign out** dialog, you can
  clear the history of your account in this browser first.

> **Warning:** The history is not a backup, and it is not encrypted. You lose
> the history in these cases:
>
> - You clear the site data of the browser.
> - A private browser session ends.
> - The browser removes stored data to free space.
> - The device fails.
>
> On a shared computer, use a separate browser profile, and clear the history
> before you leave.

Other web applications on the same address as the portal can read this
browser storage. The platform cannot delete the history in your browser.
Clearing the history does not remove the files, the sessions, or the memory of
the Agent.

## Limits and failures

| Limit | Value |
| --- | --- |
| Conversations for each account | 100 |
| Text and drafts for each account | 50 MiB |
| Messages in one conversation | 2,000 |
| Earlier message pairs in one request | 49 |
| Size of one request | 1 MiB |
| Size of the message that you enter | 64 KiB |
| Size of one reply | 4 MiB |
| Duration of one request | 10 minutes |

The connection, the first response, and the idle time have shorter limits.

When the local history is full, Chat asks you to delete conversations. It does
not delete earlier conversations by itself. When Chat cannot save the history,
you cannot send new messages, and the page shows the error. Chat does not
switch to temporary storage.

When a conversation has more earlier messages than one request can carry, the
page tells you which context it leaves out. It does not delete the history.
Chat does not summarize the conversation, and it does not send the request
again automatically.

The Agent can apply lower limits. Two tabs of the same browser profile cannot
send to the same Agent at the same time. Different devices can.

An Agent that is suspended, in deletion, held, or not ready cannot accept new
messages. The Chat page can still show the history that the browser keeps. If
the Agent cannot be reached, Chat reports the problem. Chat does not send the
request to another Agent, URL, or model.

Chat needs a secure (HTTPS) browser context with IndexedDB, Web Locks,
BroadcastChannel, and streaming fetch. If the page reports a database version
conflict, refresh the tab. Chat keeps the data and does not delete the
database.

For the HTTP contract of the image, refer to
[Agent Harness](../../reference/agent-harness/#chat). For the Agent controls,
refer to [Manage your Agent](../lifecycle/).
