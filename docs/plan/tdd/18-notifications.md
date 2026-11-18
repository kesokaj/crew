## 18. Notifications

Crew works without the operator for long stretches. When it does need the operator, it sends a mail, the mail carries the choices, and answering takes two presses (R143 to R145, R152, R157).

Notifications come by mail to start with. The phone is skipped for now: web push from Crew's own page is designed below and switched off, and no mobile app is built. Google Chat is off.

### 18.1 What is sent

| Kind | Examples | Needs an answer |
| --- | --- | --- |
| Needs you, or is about to go ahead | A project waits for its OK. A project is paused at a limit. A CLI is signed out. A release that changes code waits for Install. A ticket changes a file that is code for the CLI or for GitHub. An override of a checking role waits. A new feature will go ahead in 24 hours. A release will install in 24 hours. | Yes, or it goes ahead at the time shown |
| Something only the operator can do | Sign in to a CLI again (12.6) | Yes |
| News | A project is Done. Crew changed the model or the effort of an agent, went back from a change, or recommends one for an agent you set yourself. A comparison ended. An agent decided an open choice. The lead cancelled a ticket and replaced it. An update was installed, or stopped by its check. A subscription limit was reached, with its group of models and the time it resets. A CLI offers a new model, or no longer offers one. A hosted project went live, or stopped answering. A high-severity bug was found. | No |

- Everything that needs an answer is also in the inbox. The inbox stays the full list. A notification points at one item in it.
- An item that needs you is one mail, and one more if it is still open after four hours. (R175)
- News is collected into one mail a day. Four kinds of news are sent at once: a project that is Done, an update or a roll back that Crew made, a hosted project that stopped answering, and a high-severity bug.

### 18.2 Channels

| Channel | How | To know |
| --- | --- | --- |
| Crew's web app, later | Crew is a web page. Added to the Home Screen of the phone, the same page can notify like an app, through the browser's own push service (Web Push). Nothing is installed from a store. | Switched off until the phone is taken up. The rules for an iPhone are below. |
| Mail | Crew sends as "Crew" through your Google account with an app password, entered once in Setup and kept with the credentials (18.4). | The Google account needs 2-Step Verification. Google removes app passwords when the account's password changes. Crew then says on the inbox that the mail login was refused. |
| Google Chat | An incoming webhook of a Chat space | Not available: it needs a Business or Enterprise Google Workspace account. The channel stays in the design, switched off, for the day you have one. |

**On an iPhone, later.** When the phone is taken up, it has its own rules.

| Rule on iOS | What Crew does |
| --- | --- |
| A web page can notify only after it was added to the Home Screen, from iOS 16.4 | Setup shows the three presses: Share, Add to Home Screen, open Crew from the icon. |
| Permission can be asked only after a press on something in the page | Crew has a button, Turn on notifications. It never asks by itself. |
| A notification shows no buttons of the page's own | Pressing the notification opens Crew on that item. The alternatives are there (18.3). |
| Every message must be shown as a notification, or iOS can stop delivering | Crew sends nothing silent. |
| A device can stop getting notifications after a long quiet time | Crew sees the refused delivery, says so on the inbox, and removes the device. What needs you always goes by mail as well. |

- What needs you goes by mail, one mail for each item. News goes into one mail a day.
- Quiet hours hold news back until morning. What needs you is still sent.
- If a channel fails, Crew tries again and shows it on the inbox. Nothing depends on a notification having arrived.

### 18.3 The alternatives, and answering

A notification for an item that needs you names the same alternatives as the inbox item: Approve now, Discard, or Hold for a new feature, Install now or Hold for a release, Raise the limit, which opens the project's settings, or Stop for a project paused at its limit.

```
Crew · storefront-api
New feature: SF-31 Remember me on this device

The builder created it while working on SF-12. It is not in the plan.
Goal: a customer can stay signed in for 30 days on a device they trust.
It goes ahead by itself tomorrow at 14:20.
Approve now, discard, or hold.
```

Answering from a mail is two presses. The two other rows are for later.

| From | First press | Second press |
| --- | --- | --- |
| Later: a notification on the phone | The notification. Crew opens on that item, and the phone is signed in already. | The alternative. That decides. |
| A mail | The alternative, which is a link. Crew opens on the item with that alternative chosen. | Confirm. |
| Later: a notification with buttons, on Android or a desktop | The button. Crew opens on the item with that alternative chosen. | Confirm. |

- **Crew is reachable from where you read mail.** The ingress serves Crew on the internet behind its Google sign-in (17.2).
- **A link or a button alone never decides.** Mail scanners and link previews open links by themselves, and a notification can be pressed by accident. A decision is always a press inside Crew, signed in.
- **After the decision,** the notification is withdrawn from the other devices. An old mail link shows what was decided, and when.
- **What a message holds.** The title, the project, two or three lines of why it waits, and what the alternatives are. No secrets and no code.

### 18.4 Sending mail: the alternatives

The app password is more than a way to send. It is a login to the mailbox of that Google account, and the server keeps it. Use an account that exists only for Crew's mail, or one of the send-only ways below.

You asked whether there are other good ways than your Google account.

| Way | Needs | Good | Less good |
| --- | --- | --- | --- |
| Your Google account with an app password (chosen) | 2-Step Verification on the account | Free. Nothing else to set up. | The mail comes from your own address, shown as Crew. Google removes the password when you change the account's password, and does not recommend app passwords. |
| A mail service on your own domain, such as Amazon SES, Postmark, or Resend | A domain you own, with DNS records for it. You need a domain for hosting anyway (19.4). | The mail comes from an address like crew@your-domain. These services are made for programs that send mail. | One more account, and a price above a free amount. |
| Google's mail API with a Google sign-in | A Google Cloud project | No password is stored. | For a private account the sign-in runs out after 7 days while the project is in testing. It would have to be published and verified. Not suited to a machine that runs alone. |
| The machine sends mail itself | Nothing | | Mail from a home address is treated as spam, and many providers block it. Not recommended. |

Crew's part is the same for the first two: SMTP with an account. Changing from one to the other later is a change in Setup, not in the code.

### 18.5 How it is built (since 0.71, M18)

- **The sender.** Plain-text mail over SMTP with STARTTLS required on `notifications.mail_smtp`; a server that offers none never sees the password. A 534 or 535 answer is a refused login. Headers are Crew's own: CR and LF in a title are spaces, so an agent's title adds no header.
- **What needs you is one mail for each episode of an inbox item.** An episode begins at the first pass that lists the item's id and ends at the first that does not, so an item whose time moves, a CLI the login check finds signed out again and again, is one mail and one reminder. A pass runs every 30 seconds. A mail holds the title, the project, a few lines of Crew's own on one line, and each alternative as a link under "Choose in Crew:": a link an agent wrote stays inside that one line of text, never a line of its own. A mail of a project's OK, a pause at a limit, a full disk, a new feature, or a ticket to decide holds the item's text, cut at a sentence. A release's holds its first sentence. Every other kind holds one fixed sentence, so no file name, path, commit, tool output or error leaves Crew in a mail.
- **A link chooses, Confirm decides.** An action's link opens `/inbox?item=…&choose=…` with that alternative chosen and Confirm beside it; a link of a page opens the page. Every press of the inbox, from a mail or not, goes through `POST /api/inbox/decision`, which serves the alternative's own route with the same sign-in, so a route that asks for a fresh sign-in still does, and records what it took. An old link shows what was decided, by whom and when.
- **News.** One mail a day when quiet hours end, of the lines the Crew level's Activity has of news since the last one. A project Done, an update the clock installed or the updater switched back, and a high-severity bug go at once, read by their keys so a late row is found, and wait in quiet hours. So does a hosted project that stopped answering, once per episode (since 0.73); a deploy that went live is a line of the daily mail. News that could not go within a day is left to Activity.
- **When a mail does not go.** It is tried again after 1, 5 and 15 minutes, then every hour; after three tries the inbox says mail is not getting through. A refused login stops all sending until the account is entered again, and the inbox and Setup say so; what comes meanwhile waits and goes then. With mail off, which `needs_you` and `news` set behind a fresh sign-in, what happens meanwhile is not sent later. Two servers for a moment during an update send a message once; one that stopped in the middle of a send may send it twice, never not at all.
- **The record.** Every message is a row, written before it is sent and kept as it looked, with its state; the Notifications screen lists them. A check server starts no notifier (16.4).
