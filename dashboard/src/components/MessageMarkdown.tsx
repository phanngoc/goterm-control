import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Components } from 'react-markdown'

// The agents write GitHub-flavoured markdown — tables above all, since a
// comparison is what people most often ask for. Plain react-markdown does not
// parse GFM tables, so they rendered as one paragraph of pipes; remark-gfm
// turns them (and task lists, strikethrough, bare URLs) into real elements.
//
// Styling comes from @tailwindcss/typography's `prose`, scaled down to fit a
// chat bubble. A table wider than the bubble scrolls inside its own container
// rather than stretching the whole conversation.
const components: Components = {
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto rounded-lg ring-1 ring-gray-700/60">
      <table className="my-0 min-w-full">{children}</table>
    </div>
  ),
  // Links leave the dashboard; the chat should stay where it is.
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
}

const proseClass = [
  'prose prose-sm prose-invert max-w-none',
  // tighten the rhythm for a bubble: prose defaults are tuned for articles
  '[&_p]:my-1.5 [&_ul]:my-1.5 [&_ol]:my-1.5 [&_li]:my-0.5',
  '[&_h1]:text-base [&_h2]:text-base [&_h3]:text-sm [&_h1]:mt-3 [&_h2]:mt-3 [&_h3]:mt-2 [&_h1]:mb-1 [&_h2]:mb-1 [&_h3]:mb-1',
  // code
  '[&_pre]:bg-gray-900 [&_pre]:rounded-lg [&_pre]:p-2 [&_pre]:my-2 [&_code]:text-violet-300 [&_code]:before:content-none [&_code]:after:content-none',
  // tables: compact cells, header row set off, zebra rows
  '[&_th]:px-2.5 [&_th]:py-1.5 [&_th]:text-left [&_th]:text-xs [&_th]:uppercase [&_th]:tracking-wide [&_th]:text-gray-300 [&_th]:bg-gray-900/70',
  '[&_td]:px-2.5 [&_td]:py-1.5 [&_td]:align-top [&_td]:whitespace-nowrap',
  '[&_tbody_tr:nth-child(even)]:bg-gray-800/40 [&_tr]:border-gray-700/60',
  // quotes and rules
  '[&_blockquote]:border-gray-600 [&_blockquote]:text-gray-300 [&_hr]:border-gray-700 [&_hr]:my-3',
].join(' ')

export default function MessageMarkdown({ children }: { children: string }) {
  return (
    <div className={proseClass}>
      <Markdown remarkPlugins={[remarkGfm]} components={components}>
        {children}
      </Markdown>
    </div>
  )
}
