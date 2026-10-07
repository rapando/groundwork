import { createRouter, createWebHistory } from 'vue-router'
import { useWorkspace } from './stores/workspace'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
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
  ],
})

// Setup mode: until groundwork.yaml exists, "/" sends you to the right setup screen.
router.beforeEach(async (to) => {
  const store = useWorkspace()
  const ws = store.ws ?? (await store.refresh())
  if (!ws) return true
  if (to.path === '/' && ws.setup !== 'none') return `/setup/${ws.setup}`
  if (to.path.startsWith('/setup') && ws.setup === 'none') return '/'
  return true
})
