// The service hosts many projects; a project's UI lives under /p/<id>/ and
// its API under /api/p/<id>. Outside a project, the page is the project list.
// Switching projects is a full page load, so no store carries one project's
// state into another.
const m = location.pathname.match(/^\/p\/([a-z0-9-]+)(\/|$)/)

export const projectId = m ? m[1] : ''
export const routerBase = projectId ? `/p/${projectId}/` : '/'
export const apiBase = projectId ? `/api/p/${projectId}` : '/api'
