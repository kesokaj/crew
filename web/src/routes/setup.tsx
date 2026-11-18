import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useRef, useState } from 'react'
import { SetupTerminal, type StepResult } from '../components/Terminal'
import { FreshGate } from '../components/freshgate'
import { ApiError, getJSON, send, type AppInfo, type MailAccount, type Notification, type SetupItem, type SetupStatus } from '../lib/api'
import { Card, Check, Rows, when } from '../components/ui'

export const Route = createFileRoute('/setup')({
  component: Setup,
  validateSearch: (s: Record<string, unknown>): { step?: string } => (typeof s.step === 'string' ? { step: s.step } : {}),
})

// What Setup shows of each credential, in its order (design 12.6). A step with a terminal is signed in
// in the setup microVM; the GitHub App has a form.
const steps: { id: string; step?: string; label: string; what: string; optional?: boolean }[] = [
  { id: 'git-identity', step: 'git', label: 'Git identity', what: 'the name and mail address on every commit' },
  {
    id: 'ssh-key',
    step: 'ssh',
    label: 'Git SSH key',
    what: 'made here with ssh-keygen, or one you paste. Add its public half to your GitHub account once. Crew uses it for all git traffic. It stays with the server.',
  },
  {
    id: 'github-app',
    label: 'GitHub App',
    what: 'created once in your account, with one permission: reading contents. Crew uses its key to make a token for each sandbox, which lasts one hour.',
  },
  { id: 'gh', step: 'gh', label: 'gh', what: 'signed in with a code in the browser. Crew creates repositories, pull requests, and release pages.', optional: true },
  { id: 'gcloud', step: 'gcloud', label: 'gcloud', what: 'the login and its application default credentials. Sign in with an account meant for development.', optional: true },
  {
    id: 'agy',
    step: 'agy',
    label: 'agy',
    what: 'signed in with your Google account. An agy login can also call Google Cloud with that account\'s rights, so it should have none in a cloud project that matters.',
  },
]

function Setup() {
  const { step: asked } = Route.useSearch()
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string | null>(asked ?? null)
  // A step that ended keeps its terminal on the page until it is closed, with what it printed last.
  const [ended, setEnded] = useState(false)
  // Removing a microVM takes Kata about half a minute: Stop says so while it waits.
  const [stopping, setStopping] = useState(false)
  const [run, setRun] = useState(0)
  const [result, setResult] = useState<StepResult | null>(null)
  // A step's terminal is at the top of the page, and the terminal draws only while it is in view:
  // starting a step from lower down brings it into view.
  const card = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (open) card.current?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  }, [open, run])

  // The mail account (18.4), read on its own: Setup's credentials list does not hold it.
  const [mail, setMail] = useState<MailAccount | null>(null)
  const loadMail = useCallback(() => {
    getJSON<MailAccount>('/api/setup/mail').then(setMail, () => setMail(null))
  }, [])
  useEffect(loadMail, [loadMail])
  const load = useCallback(() => {
    getJSON<SetupStatus>('/api/setup').then(
      (s) => {
        setStatus(s)
        setError('')
      },
      (e: Error) => setError(e.message),
    )
  }, [])
  useEffect(load, [load])

  const item = (id: string): SetupItem | undefined => status?.credentials.find((c) => c.id === id)
  const onDone = (r: StepResult) => {
    setResult(r)
    setEnded(true)
    load()
  }
  const start = (step: string) => {
    setResult(null)
    setEnded(false)
    setRun((n) => n + 1)
    setOpen(step)
  }
  const close = () => {
    setOpen(null)
    setEnded(false)
  }
  // Stop closes the page's own terminal first, then asks for the removal. On dave the request was
  // lost while the server closed the terminal's connection ("Failed to fetch"), so a request that
  // got no answer is sent once more; the removal can be asked for twice.
  const stop = () => {
    close()
    setStopping(true)
    const ask = () => send('POST', '/api/setup/close')
    ask()
      .catch((e: unknown) => (e instanceof ApiError ? Promise.reject(e) : new Promise((r) => setTimeout(r, 1000)).then(ask)))
      .then(load, (e: Error) => setError('Stop: ' + e.message))
      .finally(() => setStopping(false))
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      <h1 className="text-2xl font-semibold">Setup</h1>
      <p className="mt-1 max-w-3xl text-text-dim">
        Setup comes before any project and is outside the projects. Every credential is set up once, here, in a setup microVM:
        a small sandbox that exists only for this. They are kept in Crew's database, encrypted. Each sandbox gets a copy of what
        it may have, every time one starts.
      </p>
      {error && <p className="mt-4 rounded-md border border-red-400 p-3 text-sm">{error}</p>}

      <div className="mt-6 grid gap-4 lg:grid-cols-[1fr_24rem]">
        <div className="flex flex-col gap-4">
          <div ref={card} className="scroll-mt-4">
          <Card title="Setup microVM">
            {open ? (
              <>
                <p className="mb-3 text-sm">
                  {ended ? (
                    <>
                      <b>{steps.find((s) => s.step === open)?.label ?? open}</b> ended, and the microVM is removed. The terminal shows what
                      the step printed last.
                    </>
                  ) : (
                    <>
                      Running for <b>{steps.find((s) => s.step === open)?.label ?? open}</b>. When the step ends, Crew collects what it
                      made and removes the microVM.
                    </>
                  )}
                </p>
                <FreshGate what="Setup's terminal">
                  <SetupTerminal key={`${open}-${run}`} step={open} onDone={onDone} />
                </FreshGate>
                {ended ? (
                  <button className="mt-3 rounded-md border border-border px-3 py-1 text-sm" onClick={close}>
                    Close
                  </button>
                ) : (
                  <button className="mt-3 rounded-md border border-border px-3 py-1 text-sm" onClick={stop}>
                    Stop and remove it
                  </button>
                )}
              </>
            ) : (
              <p>
                {stopping
                  ? 'Stopping: the microVM is being removed, which takes up to half a minute.'
                  : 'Not running. It starts when you sign in to something, and is removed when the login has been collected.'}
              </p>
            )}
            {result && (
              <p className={'mt-3 text-sm ' + (result.ok ? 'text-green-700 dark:text-green-400' : 'text-red-700 dark:text-red-400')}>
                {steps.find((s) => s.step === result.step)?.label ?? result.step}: {result.message}
              </p>
            )}
          </Card>
          </div>
          <Card title="What signing in does">
            <ol className="list-decimal space-y-1 pl-5 text-sm">
              <li>Crew starts the setup microVM with only that step.</li>
              <li>The tool prints a link. You approve in your own browser and paste the code.</li>
              <li>Crew collects the login into its database, encrypted, runs the login check, and removes the microVM.</li>
              <li>Runs that stopped continue in new microVMs with the new login. Waiting runs start.</li>
            </ol>
          </Card>
          {status?.publicKey && (
            <Card title="The SSH key's public half">
              <p className="mb-2 text-sm">
                Add it to your GitHub account once, at{' '}
                <a className="text-accent underline" href="https://github.com/settings/ssh/new" target="_blank" rel="noreferrer">
                  github.com/settings/ssh/new
                </a>
                .
              </p>
              <CopyText text={status.publicKey} />
            </Card>
          )}
          <GitHubAppForm set={!!item('github-app')?.set} onSaved={load} />
          <MailForm mail={mail} onSaved={loadMail} />
        </div>

        <div className="flex flex-col gap-4">
          <Card title="Setup steps">
            <ul className="space-y-3 text-sm">
              {status?.machine && (
                <li className="flex gap-2">
                  <Check on={status.machine.runtimeOK} />
                  <span>
                    <b>Machine check</b> <span className="text-text-dim">Docker {status.machine.docker}, Kata as the runtime {status.machine.runtime}</span>
                  </span>
                </li>
              )}
              {steps.map((s) => {
                const it = item(s.id)
                return (
                  <li key={s.id} className="flex gap-2">
                    <Check on={!!it?.set} />
                    <div className="flex-1">
                      <b className={s.id === 'agy' || s.id === 'gh' ? 'font-mono' : ''}>{s.label}</b>
                      {s.optional && <span className="text-text-dim">, optional</span>} <span className="text-text-dim">{s.what}</span>
                      {it?.set && (
                        <div className="text-xs text-text-dim">
                          {it.account ? `${it.account}, ` : ''}set {when(it.updatedAt)}
                        </div>
                      )}
                      {it?.unreadable && (
                        <div className="text-xs text-bad">Stored with another key file, so it cannot be read. Set it up again.</div>
                      )}
                      {s.step && (
                        <button
                          disabled={!!open && !ended}
                          className="mt-1 rounded-md border border-border px-2 py-0.5 text-xs disabled:opacity-50"
                          onClick={() => s.step && start(s.step)}
                        >
                          {it?.set ? (s.step === 'agy' ? `Sign in to ${s.step} again` : 'Set up again') : 'Set up'}
                        </button>
                      )}
                    </div>
                  </li>
                )
              })}
              <li className="flex gap-2">
                <Check on={mail?.state === 'ok'} />
                <div className="flex-1">
                  <b>Mail</b> <span className="text-text-dim">a Google account with an app password, for notifications. Its form is on the left.</span>
                  <div className="text-xs text-text-dim" data-mail-state>
                    {mailWords(mail)}
                  </div>
                </div>
              </li>
            </ul>
            <p className="mt-3 text-xs text-text-dim">
              A CLI that is not signed in is switched off. Until one is signed in, no run starts. Everything Crew has saved stays.
            </p>
          </Card>
          <Card title="What a sandbox gets when it starts">
            <Rows
              rows={[
                ['A run of a project', 'the login of the one CLI it runs, the git identity, and a GitHub token that can only read and lasts one hour. No gcloud. Copied in, not linked, so no sandbox shares a file with another.'],
                ['Never in a sandbox', 'the SSH key and gh'],
                ['The improver', 'only its CLI login'],
                ['A conversation about a project', 'what a run of that project gets'],
                ['The chat of a ticket', 'only its CLI login and the read-only GitHub token'],
                ['A check that runs commands', 'only the read-only GitHub token'],
                ['A hosted project', 'none of them'],
                ['After the run', 'gone with the sandbox and its home. The token is revoked.'],
              ]}
            />
          </Card>
          <Card title="How Crew notices">
            <Rows
              rows={[
                ['Before a run', 'the run driver asks the CLI before it starts it'],
                ['During a run', 'a login error ends the run with the reason login. The work is kept.'],
                ['On a schedule', 'every 30 minutes in a small check microVM, with one real request'],
                ['Signed out', 'after two refused checks in a row'],
              ]}
            />
          </Card>
        </div>
      </div>
    </div>
  )
}

// The public half of the SSH key, with a button that copies it. Where the browser does not let the page
// write the clipboard, the text is selected instead, for Ctrl+C.
function CopyText({ text }: { text: string }) {
  const pre = useRef<HTMLPreElement>(null)
  const [msg, setMsg] = useState('')
  const select = () => {
    const sel = window.getSelection()
    if (!pre.current || !sel) return
    sel.selectAllChildren(pre.current)
    setMsg('Selected: press Ctrl+C to copy it.')
  }
  return (
    <>
      <pre ref={pre} className="overflow-x-auto rounded-md bg-bg p-2 text-xs">
        {text}
      </pre>
      <button
        className="mt-2 rounded-md border border-border px-3 py-1 text-sm"
        onClick={() => {
          if (!navigator.clipboard) return select()
          navigator.clipboard.writeText(text).then(() => setMsg('Copied.'), select)
        }}
      >
        Copy
      </button>
      {msg && <span className="ml-2 text-sm text-text-dim">{msg}</span>}
    </>
  )
}

// The GitHub App: created on GitHub, in your own account, with one permission, then its id and key
// file given here (design 12.6). The key file is read in the browser and sent once.
function GitHubAppForm({ set, onSaved }: { set: boolean; onSaved: () => void }) {
  const [appId, setAppId] = useState('')
  const [key, setKey] = useState('')
  const [msg, setMsg] = useState('')
  const host = typeof location === 'undefined' ? '' : location.host
  const create =
    'https://github.com/settings/apps/new?' +
    new URLSearchParams({
      name: `crew-${host.split('.')[0] ?? 'crew'}`,
      description: 'Crew: read-only tokens for its sandboxes',
      url: `https://${host}`,
      public: 'false',
      webhook_active: 'false',
      contents: 'read',
    }).toString()
  return (
    <Card title="GitHub App">
      <ol className="mb-3 list-decimal space-y-1 pl-5 text-sm">
        <li>
          <a className="text-accent underline" href={create} target="_blank" rel="noreferrer">
            Create the App on GitHub
          </a>
          , private, with one permission: Contents, read-only. Nothing else.
        </li>
        <li>Install it on your account, for all repositories.</li>
        <li>On the App's page, generate a private key. GitHub gives you a .pem file.</li>
        <li>Give its App ID and the key file here.</li>
      </ol>
      {set && <p className="mb-2 text-sm text-text-dim">An App is set up. Giving another replaces it.</p>}
      <form
        className="flex flex-col gap-2 text-sm"
        onSubmit={(e) => {
          e.preventDefault()
          setMsg('Asking GitHub…')
          send<AppInfo>('POST', '/api/setup/github-app', { appId, key }).then(
            (info) => {
              setMsg(`Stored: ${info?.name ?? 'the App'}, installed on ${info?.installations.join(', ') || 'no account yet'}.`)
              setKey('')
              onSaved()
            },
            (err: Error) => setMsg(err.message),
          )
        }}
      >
        <label>
          App ID <input className="ml-2 w-32 rounded border border-border bg-bg px-2 py-1" value={appId} onChange={(e) => setAppId(e.target.value)} inputMode="numeric" />
        </label>
        <label>
          Key file{' '}
          <input
            type="file"
            accept=".pem"
            className="text-sm file:mr-2 file:cursor-pointer file:rounded-md file:border file:border-solid file:border-border file:bg-transparent file:px-3 file:py-1 file:text-sm file:text-text"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) void f.text().then(setKey)
            }}
          />
        </label>
        <button disabled={!appId || !key} className="w-fit rounded-md border border-border px-3 py-1 disabled:opacity-50">
          Check with GitHub and store
        </button>
        {msg && <p className="text-text-dim">{msg}</p>}
      </form>
    </Card>
  )
}

// mailWords says where the mail account stands (D21).
function mailWords(m: MailAccount | null): string {
  if (!m) return 'Not read yet.'
  switch (m.state) {
    case 'none':
      return m.error ? `Not set up: ${m.error}.` : 'Not set up.'
    case 'untested':
      return `${m.account}, stored; nothing sent with it yet.`
    case 'ok':
      return `${m.account}, the last message went ${m.okAt ? when(m.okAt) : ''}.`
    case 'refused':
      return `${m.account}: the login was refused${m.refusedAt ? ` ${when(m.refusedAt)}` : ''}: ${m.error ?? ''}. Nothing is sent until you enter it again.`
  }
}

// The mail account (18.2, 18.4): the address and an app password of that Google account, sent once
// behind a fresh sign-in and never shown again. A test message goes to notifications.mail_to.
function MailForm({ mail, onSaved }: { mail: MailAccount | null; onSaved: () => void }) {
  const [account, setAccount] = useState('')
  const [password, setPassword] = useState('')
  const [msg, setMsg] = useState('')
  const [testing, setTesting] = useState(false)
  return (
    <Card title="Mail">
      <ol className="mb-3 list-decimal space-y-1 pl-5 text-sm">
        <li>Use a Google account with 2-Step Verification, best one that exists only for Crew's mail: an app password is a login to its mailbox, and the server keeps it.</li>
        <li>
          Make an app password at{' '}
          <a className="text-accent underline" href="https://myaccount.google.com/apppasswords" target="_blank" rel="noreferrer">
            myaccount.google.com/apppasswords
          </a>
          , and give it here with the account's address. Google removes it when the account's password changes.
        </li>
      </ol>
      {mail?.account && <p className="mb-2 text-sm text-text-dim">{mailWords(mail)} Giving another replaces it.</p>}
      <form
        className="flex flex-col gap-2 text-sm"
        data-mail-form
        onSubmit={(e) => {
          e.preventDefault()
          setMsg('Storing…')
          send<MailAccount>('PUT', '/api/setup/mail', { account, password }).then(
            () => {
              setMsg('Stored. Send a test message to see that it works.')
              setPassword('')
              onSaved()
            },
            (err: Error) => setMsg(err.message),
          )
        }}
      >
        <label>
          Account{' '}
          <input
            className="ml-2 w-64 rounded border border-border bg-bg px-2 py-1"
            type="email"
            autoComplete="off"
            value={account}
            onChange={(e) => setAccount(e.target.value)}
            placeholder="you@example.com"
          />
        </label>
        <label>
          App password{' '}
          <input
            className="ml-2 w-64 rounded border border-border bg-bg px-2 py-1"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <div className="flex gap-2">
          <button disabled={!account || !password} className="w-fit rounded-md border border-border px-3 py-1 disabled:opacity-50">
            Store
          </button>
          <button
            type="button"
            disabled={!mail?.account || testing}
            className="w-fit rounded-md border border-border px-3 py-1 disabled:opacity-50"
            onClick={() => {
              setTesting(true)
              setMsg('Sending a test message…')
              send<Notification>('POST', '/api/notifications/test').then(
                (row) => {
                  setMsg(row?.state === 'sent' ? 'The test message went.' : `The test message did not go: ${row?.error ?? 'unknown'}.`)
                  setTesting(false)
                  onSaved()
                },
                (err: Error) => {
                  setMsg(err.message)
                  setTesting(false)
                },
              )
            }}
          >
            Send a test message
          </button>
        </div>
        {msg && <p className="text-text-dim">{msg}</p>}
      </form>
    </Card>
  )
}
