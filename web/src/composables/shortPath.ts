/**
 * A machine path short enough for a header or a list row: the home folder
 * becomes ~, macOS's per-user temp folder becomes …, and anything still longer
 * than max keeps its last segments behind "…/". Show the full path in a title.
 */
export function shortPath(p: string, max = 48): string {
  let s = p.replace(/^\/(?:Users|home)\/[^/]+(?=\/|$)/, '~').replace(/^(?:\/private)?\/var\/folders\/[^/]+\/[^/]+\//, '…/')
  if (s.length <= max) return s
  const parts = s.split('/')
  let tail = parts.pop() ?? ''
  while (parts.length && parts[parts.length - 1] !== '…' && `…/${parts[parts.length - 1]}/${tail}`.length <= max) tail = `${parts.pop()}/${tail}`
  s = `…/${tail}`
  return s
}
