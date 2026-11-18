import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type MailView, type Notification } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Card, Failed, PageHead, Rows, Tag, when } from '../components/ui'

export const Route = createFileRoute('/notifications')({ component: Notifications })

// Notifications (design 18, notify.png): the mail, what is sent and when, the phone for later, a mail
// as it looks, and every message Crew sent or held back. The settings are Machine settings' keys; this
// page shows them and links there (plan D16).
function Notifications() {
  const { data, error, reload } = usePoll<{ mail: MailView; sent: Notification[] }>('/api/notifications', 10000)
  const [testing, setTesting] = useState(false)
  const [said, setSaid] = useState('')
  const test = () => {
    setTesting(true)
    setSaid('Sending a test message…')
    send<Notification>('POST', '/api/notifications/test').then(
      (row) => {
        setSaid(row?.state === 'sent' ? `The test message went to ${data?.mail.to}.` : `The test message did not go: ${row?.error ?? 'unknown'}.`)
        setTesting(false)
        reload()
      },
      (e: Error) => {
        setSaid(e.message)
        setTesting(false)
      },
    )
  }
  const mail = data?.mail
  const sample = data?.sent.find((m) => m.kind === 'needs_you' && m.open)
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        title="Notifications"
        actions={
          <button className={button} disabled={testing} onClick={test}>
            Send a test message
          </button>
        }
      >
        When something needs you, Crew sends a mail. The phone is skipped for now, and there is no mobile app. Answering takes two presses:
        the alternative in the mail, then Confirm in Crew. Everything is also in the inbox.
      </PageHead>
      <Failed error={error} />
      {said && (
        <p className="mb-3 text-sm" data-test-said>
          {said}
        </p>
      )}
      {mail && (
        <div className="grid gap-4 md:grid-cols-3">
          <Card title="Mail" right={<Tag tone={mail.on ? 'blue' : 'bad'}>{mail.on ? 'On' : 'Off'}</Tag>}>
            <div data-mail-card>
              <Rows
                narrow
                rows={[
                  ['To', <span className="font-mono">{mail.to || 'nobody: notifications.mail_to is empty'}</span>],
                  ['From', mail.account ? `${mail.fromName}, sent through ${mail.account} with an app password` : 'no account: set one up in Setup'],
                  ['If refused', 'the inbox says so, and Setup shows the mail account as failed'],
                ]}
              />
              {!mail.on && mail.why && <p className="mt-3 text-sm text-bad">Off: {mail.why}.</p>}
            </div>
          </Card>
          <Card title="What is sent, and when">
            <Rows
              narrow
              rows={[
                ['What needs you', mail.itemsOn ? 'one mail for each item' : 'no mail: notifications.needs_you has no mail'],
                ['Sign-ins', 'one mail, as an item that needs you'],
                [
                  'News',
                  mail.newsOn
                    ? 'one mail a day, when quiet hours end. At once: a project that is Done, an update or a roll back Crew made, a high-severity bug'
                    : 'no mail: notifications.news is not digest',
                ],
                ['Reminder', `once, after ${mail.remindAfterHours} hours, if the item is still open`],
                ['Quiet hours', `${mail.quietHours.replace('-', ' to ')}. News waits. What needs you is still sent.`],
              ]}
            />
            <p className="mt-3 text-xs text-text-dim">
              Change them in <Link to="/settings" className="text-accent underline">Machine settings</Link>.
            </p>
          </Card>
          <Card title="Web push, to a phone" right={<Tag>later</Tag>}>
            <p className="text-sm text-text-dim">
              Switched off. Crew is a web page, and on the Home Screen of a phone it could notify like an app. You skipped the phone for now, so
              this is designed and not built.
            </p>
          </Card>
        </div>
      )}
      <p className="mt-4 text-sm text-text-dim">
        <b className="text-text">Google Chat</b> is off. A Chat webhook needs a Google Workspace account, and you have a private Google account. It
        can be switched on the day you have one.
      </p>
      <div className="mt-6 grid gap-4 md:grid-cols-[2fr_1fr]">
        <div>
          <h2 className="mb-2 text-lg font-semibold">
            A mail, as it looks <span className="text-sm font-normal text-text-dim">the newest open one</span>
          </h2>
          {sample ? (
            <section data-sample className="rounded-lg border border-border border-l-4 border-l-accent bg-bg-raised p-4">
              <div className="flex text-xs text-text-dim">
                <span>
                  <b className="text-text">Crew</b> · {sample.project || 'crew'} · {when(sample.createdAt)}
                </span>
                <span className="ml-auto">
                  <Tag>mail</Tag>
                </span>
              </div>
              <pre className="mt-2 font-sans text-sm whitespace-pre-wrap">{sample.body}</pre>
            </section>
          ) : (
            <p className="text-sm text-text-dim">Nothing waits for you now.</p>
          )}
        </div>
        <Card title="Answering takes two presses">
          <ol className="list-decimal space-y-1 pl-5 text-sm">
            <li>Press the alternative in the mail. It is a link, and Crew opens on the item with it chosen.</li>
            <li>Press Confirm. A link alone never decides, because mail scanners open links.</li>
          </ol>
        </Card>
      </div>
      <h2 className="mt-6 mb-2 text-lg font-semibold">
        Sent <span className="text-sm font-normal text-text-dim">{data?.sent.length ?? 0} kept</span>
      </h2>
      <table className="w-full text-sm" data-sent>
        <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
          <tr>
            <th className="py-1">When</th>
            <th>By</th>
            <th>Message</th>
            <th>About</th>
            <th>State</th>
          </tr>
        </thead>
        <tbody>
          {data?.sent.map((m) => (
            <tr key={m.id} data-row={m.id} className="border-t border-border align-top">
              <td className="py-2 pr-3 whitespace-nowrap">{when(m.sentAt ?? m.createdAt)}</td>
              <td className="pr-3">mail</td>
              <td className="pr-3">
                <div>{m.subject.replace(/^(Still waiting: )?Crew · [^·]+ · /, '$1')}</div>
                <div className="text-text-dim">{firstLine(m)}</div>
              </td>
              <td className="pr-3 font-mono">{m.project || 'crew'}</td>
              <td data-state>{stateWords(m)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// firstLine is the message's first line of its own, after the title and the project.
function firstLine(m: Notification): string {
  const lines = m.body.split('\n').filter((l) => l.trim() !== '')
  return lines[m.kind === 'needs_you' || m.kind === 'reminder' ? 2 : 1] ?? ''
}

// stateWords says where a message stands (notify.png's STATE): one that waits for you, news, or why it
// did not go.
function stateWords(m: Notification): string {
  switch (m.state) {
    case 'queued':
    case 'sending':
      return m.error ? `not sent yet: ${m.error}` : 'not sent yet'
    case 'failed':
      return `failed: ${m.error ?? ''}`
    case 'dropped':
      return `not sent: ${m.error ?? ''}`
  }
  if (m.kind === 'news' || m.kind === 'digest') return 'news'
  if (m.kind === 'test') return 'test'
  return m.open ? 'waits for you' : 'no longer waits'
}
