import { createRouter, createWebHistory } from 'vue-router'
import { useWorkspace } from './stores/workspace'
import { projectId, routerBase } from './project'

const projectRoutes = [
  { path: '/', component: () => import('./views/HomeView.vue') },
  { path: '/runs/:id(\\d+)/plan', component: () => import('./views/PlanView.vue') },
  { path: '/runs/:id(\\d+)?', component: () => import('./views/RunsView.vue') },
  { path: '/graph', component: () => import('./views/GraphView.vue') },
  { path: '/inventory', component: () => import('./views/InventoryView.vue') },
  { path: '/variables', component: () => import('./views/VariablesView.vue') },
  { path: '/troubleshoot/:id(\\d+)?', component: () => import('./views/TroubleshootView.vue') },
  { path: '/code/:path(.*)*', component: () => import('./views/CodeView.vue') },
  { path: '/setup/detect', component: () => import('./views/DetectView.vue') },
  { path: '/setup/scaffold', component: () => import('./views/ScaffoldView.vue') },
  { path: '/:rest(.*)*', component: () => import('./views/NotFound.vue') },
]

// Outside /p/<id>/ the only page is the project list.
const serviceRoutes = [
  { path: '/', component: () => import('./views/ProjectsView.vue') },
  { path: '/:rest(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(routerBase),
  routes: projectId ? projectRoutes : serviceRoutes,
})

// Setup mode: until groundwork.yaml exists, "/" sends you to the right setup screen.
router.beforeEach(async (to) => {
  if (!projectId) return true
  const store = useWorkspace()
  const ws = store.ws ?? (await store.refresh())
  if (!ws) return true
  if (to.path === '/' && ws.setup !== 'none') return `/setup/${ws.setup}`
  if (to.path.startsWith('/setup') && ws.setup === 'none') return '/'
  return true
})
