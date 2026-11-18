import { createFileRoute, Link } from '@tanstack/react-router'
import { usePoll } from '../lib/poll'
import { button, Failed, PageHead, when } from '../components/ui'

export const Route = createFileRoute('/database')({ component: Database })

type DB = {
  version: string
  sizeBytes: number
  groups: { name: string; kept: string; tables: Record<string, number>; rows: number }[]
  lastStep?: number
  lastStepAt?: string
  stepName?: string
  backups: string[]
  backupDir: string
  backupError?: string
  changes: { seq: number; at: string; project?: string; who: string; table: string; row: string; after: unknown }[]
  // The data folder's disk, from the disk guard's readings (design 16.10).
  disk?: { at: string; usedBytes: number; totalBytes: number; percent: number; growPerMonth?: number; monthsLeft?: number }
}

function size(b: number): string {
  if (b > 1 << 30) return `${(b / (1 << 30)).toFixed(1)} GB`
  return `${Math.round(b / (1 << 20))} MB`
}

// The Database screen (design 15.3, 20.1, db.png): the container, every group of tables with its
// rows and what is kept for how long, what is not in it, the backups, and the latest changes. It
// reads, and changes nothing.
function Database() {
  const { data: d, error } = usePoll<DB>('/api/database', 15000)
  // Newest first, as the API lists them.
  const days = (d?.backups ?? []).filter((f) => f.startsWith('crew-'))
  const months = (d?.backups ?? []).filter((f) => f.startsWith('events-')).map((f) => f.slice(7, 14))
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Database">
        The database is the brain of Crew. Everything Crew saves is here: the plans, the tickets, the records, the settings, every
        conversation, and every trajectory. The server keeps no state of its own. Git holds the code. This screen only reads.
      </PageHead>
      <Failed error={error} />
      {d && (
        <>
          <div className="grid gap-3 md:grid-cols-3">
            <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
              <p className="text-xs tracking-wide text-text-dim uppercase">The container</p>
              <p className="my-1 text-2xl font-semibold">crew-db</p>
              <dl className="grid grid-cols-[6.5rem_1fr] gap-y-1">
                <dt className="text-text-dim">Engine</dt>
                <dd>PostgreSQL {d.version}</dd>
                <dt className="text-text-dim">Runs</dt>
                <dd>in its own container on the machine, beside the server</dd>
                <dt className="text-text-dim">Size</dt>
                <dd>{size(d.sizeBytes)}</dd>
                <dt className="text-text-dim">Disk</dt>
                <dd data-disk>
                  {d.disk ? (
                    <>
                      {d.disk.percent.toFixed(0)}% of {size(d.disk.totalBytes)} used
                      {d.disk.growPerMonth !== undefined && d.disk.monthsLeft !== undefined
                        ? `, growing ${size(d.disk.growPerMonth)} a month: ${d.disk.monthsLeft.toFixed(0)} months of room before Crew stops at its limit`
                        : ', not growing in the last 30 days'}
                      . Crew cleans at 85% and starts no new run at 95%.
                    </>
                  ) : (
                    'not read yet: the disk guard reads it every 10 minutes'
                  )}
                </dd>
                <dt className="text-text-dim">Reached by</dt>
                <dd>the server and the updater, and nothing else</dd>
                <dt className="text-text-dim">Sandboxes</dt>
                <dd>no route to it. An agent asks the Crew MCP, which answers only for its own project.</dd>
              </dl>
            </section>
            <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
              <p className="mb-2 text-xs tracking-wide text-text-dim uppercase">What is not in it</p>
              <dl className="grid grid-cols-[6.5rem_1fr] gap-y-1">
                <dt className="text-text-dim">Git</dt>
                <dd>the code, the documentation, and a project's own skills and context</dd>
                <dt className="text-text-dim">One key file</dt>
                <dd>encrypts the credentials in the database. It is not in the database and not in a backup.</dd>
                <dt className="text-text-dim">While a run goes</dt>
                <dd>its home and the spool of its events. Both are removed with the sandbox.</dd>
              </dl>
            </section>
            <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
              <p className="mb-2 text-xs tracking-wide text-text-dim uppercase">Backups, and changes to the schema</p>
              <dl className="grid grid-cols-[6.5rem_1fr] gap-y-1">
                <dt className="text-text-dim">Backup</dt>
                <dd data-backups>
                  one dump every night, inside the day's tar.gz in <span className="font-mono text-xs">{d.backupDir || 'the backups folder'}</span>,
                  with the repositories of projects that have no remote.{' '}
                  {d.backupError
                    ? `It cannot be read: ${d.backupError}`
                    : days.length > 0
                      ? `The last is ${days[0]}, ${days.length} kept.`
                      : 'None yet.'}
                </dd>
                <dt className="text-text-dim">Months</dt>
                <dd data-months>
                  the events of each month that has ended, in a file of their own, kept for good: {months.length > 0 ? months.join(', ') : 'none yet'}.
                </dd>
                <dt className="text-text-dim">Snapshot</dt>
                <dd>one dump before every update, kept with each kept version (Machine settings, Updates: 5 unless set)</dd>
                <dt className="text-text-dim">Schema</dt>
                <dd>
                  step {d.lastStep}, {d.stepName}, applied {when(d.lastStepAt)}. Changed only by a release, and tried on a scratch copy first.
                </dd>
                <dt className="text-text-dim">Look inside</dt>
                <dd>
                  <span className="font-mono text-xs">crew-ctl db shell</span>, with a role that can only read
                </dd>
                <dt className="text-text-dim">If the disk dies</dt>
                <dd>everything here is lost, unless the backups are copied off this machine. Code on GitHub survives.</dd>
              </dl>
              <div className="mt-3 flex gap-2">
                <Link to="/versions" className={button}>
                  Versions
                </Link>
              </div>
            </section>
          </div>
          <h2 className="mt-6 mb-2 text-xl font-semibold">
            Tables <span className="text-sm font-normal text-text-dim">every row that belongs to a project carries it</span>
          </h2>
          <table className="w-full text-sm">
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr className="border-b border-border">
                <th className="py-1">Group</th>
                <th>Tables</th>
                <th className="text-right">Rows</th>
                <th className="pl-4">Kept</th>
              </tr>
            </thead>
            <tbody>
              {d.groups.map((g) => (
                <tr key={g.name} className="border-b border-border/60 align-top" data-group={g.name}>
                  <td className="py-1.5 pr-2">{g.name}</td>
                  <td className="py-1.5 pr-2 font-mono text-xs">
                    {Object.entries(g.tables)
                      .map(([t, n]) => `${t} ${n.toLocaleString()}`)
                      .join(', ')}
                  </td>
                  <td className="py-1.5 text-right">{g.rows.toLocaleString()}</td>
                  <td className="py-1.5 pl-4 text-text-dim">{g.kept}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="mt-1 text-xs text-text-dim">Rows as PostgreSQL counts them, close to the true number and read without a scan.</p>
          <h2 className="mt-6 mb-2 text-xl font-semibold">
            Latest changes <span className="text-sm font-normal text-text-dim">every change to a ticket, a record, or a setting is a row, and rows are only added; a deleted project's go with it</span>
          </h2>
          <table className="w-full text-sm">
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr className="border-b border-border">
                <th className="py-1 pr-3">Change</th>
                <th className="pr-3">Project</th>
                <th className="pr-3">What</th>
                <th className="pr-3">Who</th>
                <th>When</th>
              </tr>
            </thead>
            <tbody>
              {d.changes.map((c) => (
                <tr key={c.seq} className="border-b border-border/60 align-top">
                  <td className="py-1 pr-3 font-mono text-xs text-text-dim whitespace-nowrap">c_{String(c.seq).padStart(6, '0')}</td>
                  <td className="py-1 pr-3 whitespace-nowrap">{c.project ?? '—'}</td>
                  <td className="py-1 pr-3">
                    {c.table} <span className="font-mono text-xs">{c.row}</span>
                    {c.after != null && <div className="max-w-xl truncate font-mono text-xs text-text-dim">{JSON.stringify(c.after)}</div>}
                  </td>
                  <td className="py-1 pr-3 whitespace-nowrap">{c.who}</td>
                  <td className="py-1 whitespace-nowrap">{when(c.at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}
