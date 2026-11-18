import { createFileRoute, Link } from '@tanstack/react-router'
import { PageHead } from '../components/ui'
import { Feed } from '../components/activity'

export const Route = createFileRoute('/p/$project/activity')({ component: Activity })

// Activity (design 20.1, activity.png): what agents are doing in the project, newest first.
function Activity() {
  const { project } = Route.useParams()
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
          </>
        }
        title="Activity"
      >
        What agents are doing in {project}, one line per event, newest first. Every line is read from a move, a run, a check, or a
        decision in Crew's database.
      </PageHead>
      <Feed project={project} />
    </div>
  )
}
