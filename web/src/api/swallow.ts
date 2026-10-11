/**
 * swallow is for promises whose failure has nowhere better to go than the
 * console: background refreshes that the next event retries, optional
 * decorations. It names the reason so a swallowed error is still traceable
 * in development; in production it is silent.
 */
export function swallow(why: string): (e: unknown) => void {
  return (e: unknown) => {
    if (import.meta.env.DEV) console.warn(`${why}:`, e)
  }
}
