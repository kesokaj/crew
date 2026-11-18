import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Card, Failed, PageHead, Rows, Tag, ago, button, mainButton } from '../components/ui'

export const Route = createFileRoute('/storage')({ component: StorageRuntimes })

type StorageStatusView = {
  stateDb: {
    mode: 'postgres' | 'sqlite' | string
    postgresHealthy: boolean
    sqlite: {
      path: string
      walPath: string
      journalMode: string
      walFrames: number
      checkpoints: number
      trajectoriesCount: number
      blobsCount: number
      walBytes: number
      lastCheckpoint?: string
    }
  }
  blobStore: {
    driver: 'localfs' | 'gcs' | string
    gcsBucket?: string
    healthy: boolean
    error?: string
    objects: number
    bytes: number
    trajectoriesTotal: number
    trajectoriesOffloaded: number
    recent: {
      key: string
      projectId?: string
      runId?: string
      kind: string
      driver: string
      sha256: string
      sizeBytes: number
      contentType: string
      createdAt: string
    }[]
  }
  computeRuntime: {
    driver: string
    kubernetesSeam: boolean
    projectVMs: number
    snapshots: number
  }
}

function StorageRuntimes() {
  const { data, error, reload } = usePoll<StorageStatusView>('/api/storage/status', 5000)
  const [bucket, setBucket] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [busy, setBusy] = useState(false)

  const saveConfig = async (next: { stateDbMode: string; blobDriver: string; gcsBucket?: string; checkpoint?: boolean }) => {
    setBusy(true)
    setActionErr('')
    try {
      await send('PUT', '/api/storage/config', next)
      reload()
    } catch (e) {
      setActionErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-6xl space-y-5 p-6">
      <PageHead
        crumbs={
          <Link to="/" className="text-accent">
            All projects
          </Link>
        }
        title="Runtimes & Storage"
      >
        ComputeRuntime isolation, StateDB persistence (PostgreSQL or embedded SQLite WAL), and immutable BlobStore trajectory archives.
      </PageHead>

      <Failed error={error || actionErr} />

      {!data ? (
        <p className="text-sm text-text-dim">Loading runtime and storage status…</p>
      ) : (
        <>
          <div className="grid gap-4 lg:grid-cols-3">
            <Card title="ComputeRuntime">
              <Rows
                rows={[
                  ['Driver', <Tag tone="blue">{data.computeRuntime.driver}</Tag>],
                  ['Kubernetes seam', data.computeRuntime.kubernetesSeam ? 'RuntimeClass: kata ready' : 'disabled'],
                  ['Project VMs', String(data.computeRuntime.projectVMs)],
                  ['Sealed snapshots', String(data.computeRuntime.snapshots)],
                ]}
              />
            </Card>

            <Card
              title="StateDB"
              right={<Tag tone={data.stateDb.mode === 'sqlite' ? 'amber' : 'ok'}>{data.stateDb.mode.toUpperCase()}</Tag>}
            >
              <Rows
                rows={[
                  ['PostgreSQL', data.stateDb.postgresHealthy ? <Tag tone="ok">healthy</Tag> : <Tag tone="bad">offline</Tag>],
                  ['SQLite journal', <span className="font-mono">{data.stateDb.sqlite.journalMode || 'WAL'}</span>],
                  ['WAL frames / checkpoints', `${data.stateDb.sqlite.walFrames} / ${data.stateDb.sqlite.checkpoints}`],
                  ['WAL mirrored rows', `${data.stateDb.sqlite.trajectoriesCount} runs · ${data.stateDb.sqlite.blobsCount} blobs`],
                  ['WAL file size', `${data.stateDb.sqlite.walBytes} B`],
                ]}
              />
              <div className="mt-4 flex flex-wrap gap-2">
                <button
                  type="button"
                  data-set-statedb="postgres"
                  disabled={busy}
                  className={data.stateDb.mode === 'postgres' ? mainButton : button}
                  onClick={() => void saveConfig({ stateDbMode: 'postgres', blobDriver: data.blobStore.driver, gcsBucket: data.blobStore.gcsBucket })}
                >
                  PostgreSQL
                </button>
                <button
                  type="button"
                  data-set-statedb="sqlite"
                  disabled={busy}
                  className={data.stateDb.mode === 'sqlite' ? mainButton : button}
                  onClick={() =>
                    void saveConfig({ stateDbMode: 'sqlite', blobDriver: data.blobStore.driver, gcsBucket: data.blobStore.gcsBucket, checkpoint: true })
                  }
                >
                  SQLite WAL
                </button>
                <button
                  type="button"
                  data-checkpoint-wal
                  disabled={busy}
                  className={button}
                  onClick={() =>
                    void saveConfig({
                      stateDbMode: data.stateDb.mode,
                      blobDriver: data.blobStore.driver,
                      gcsBucket: data.blobStore.gcsBucket,
                      checkpoint: true,
                    })
                  }
                >
                  Checkpoint WAL
                </button>
              </div>
            </Card>

            <Card
              title="BlobStore"
              right={<Tag tone={data.blobStore.healthy ? 'ok' : 'amber'}>{data.blobStore.driver}</Tag>}
            >
              <Rows
                rows={[
                  ['Driver', data.blobStore.driver === 'gcs' ? 'GCS Object Store' : 'Local FS (.jsonl.zst)'],
                  ['Offloaded runs', `${data.blobStore.trajectoriesOffloaded} / ${data.blobStore.trajectoriesTotal}`],
                  ['Stored blobs', `${data.blobStore.objects} (${data.blobStore.bytes} B)`],
                  ['GCS bucket', <span data-free-text>{data.blobStore.gcsBucket || '—'}</span>],
                ]}
              />
              <div className="mt-4 space-y-2">
                <div className="flex flex-wrap gap-2">
                  <button
                    type="button"
                    data-set-blobdriver="localfs"
                    disabled={busy}
                    className={data.blobStore.driver === 'localfs' ? mainButton : button}
                    onClick={() => void saveConfig({ stateDbMode: data.stateDb.mode, blobDriver: 'localfs' })}
                  >
                    Local FS
                  </button>
                  <button
                    type="button"
                    data-set-blobdriver="gcs"
                    disabled={busy}
                    className={data.blobStore.driver === 'gcs' ? mainButton : button}
                    onClick={() =>
                      void saveConfig({
                        stateDbMode: data.stateDb.mode,
                        blobDriver: 'gcs',
                        gcsBucket: bucket || data.blobStore.gcsBucket || 'crew-artifacts',
                      })
                    }
                  >
                    GCS Seam
                  </button>
                </div>
                <input
                  aria-label="GCS Bucket"
                  placeholder="Optional GCS bucket name"
                  value={bucket}
                  onChange={(e) => setBucket(e.target.value)}
                  className="w-full rounded-md border border-border bg-bg px-2.5 py-1 text-xs"
                />
              </div>
            </Card>
          </div>

          <Card title="Recent Immutable Blob Objects (SHA-256)">
            {data.blobStore.recent.length === 0 ? (
              <p className="text-sm text-text-dim">No offloaded trajectory blobs yet. Archive any trajectory on its project Trajectories screen.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm" aria-label="Recent blob objects">
                  <thead>
                    <tr className="border-b border-border text-left text-xs text-text-dim uppercase">
                      <th className="py-1.5 pr-3">Object Key</th>
                      <th className="py-1.5 pr-3">Kind</th>
                      <th className="py-1.5 pr-3">Driver</th>
                      <th className="py-1.5 pr-3">SHA-256</th>
                      <th className="py-1.5 pr-3">Bytes</th>
                      <th className="py-1.5">Stored</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.blobStore.recent.map((b) => (
                      <tr key={b.key} className="border-b border-border last:border-b-0">
                        <td className="py-1.5 pr-3 font-mono text-xs" data-free-text>
                          {b.key}
                        </td>
                        <td className="py-1.5 pr-3">{b.kind}</td>
                        <td className="py-1.5 pr-3 font-mono text-xs">{b.driver}</td>
                        <td className="py-1.5 pr-3 font-mono text-xs" data-free-text>
                          {b.sha256.slice(0, 16)}…
                        </td>
                        <td className="py-1.5 pr-3 font-mono text-xs tabular-nums">{b.sizeBytes}</td>
                        <td className="py-1.5 text-xs text-text-dim">{ago(b.createdAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}
    </div>
  )
}
