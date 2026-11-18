import { createFileRoute } from '@tanstack/react-router'
import { ChatRoomsView } from '../components/chat-rooms'

export const Route = createFileRoute('/p/$project/chat')({ component: ProjectChat })

function ProjectChat() {
  const { project } = Route.useParams()
  return <ChatRoomsView project={project} />
}
