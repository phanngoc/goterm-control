/// <reference types="vite/client" />
// Monaco, bundled with the dashboard rather than fetched from a CDN at run
// time: the page is served from this machine through a tunnel, and an editor
// that silently fails to load when jsDelivr is slow or blocked is worse than a
// bigger chunk. This module is only imported by the lazily loaded editor, so
// nobody downloads it until they open a project's files.
import * as monaco from 'monaco-editor'
import { loader } from '@monaco-editor/react'
import EditorWorker from 'monaco-editor/editor/editor.worker?worker'
import JsonWorker from 'monaco-editor/language/json/json.worker?worker'
import CssWorker from 'monaco-editor/language/css/css.worker?worker'
import HtmlWorker from 'monaco-editor/language/html/html.worker?worker'
import TsWorker from 'monaco-editor/language/typescript/ts.worker?worker'

self.MonacoEnvironment = {
  getWorker(_id: string, label: string) {
    switch (label) {
      case 'json': return new JsonWorker()
      case 'css': case 'scss': case 'less': return new CssWorker()
      case 'html': case 'handlebars': case 'razor': return new HtmlWorker()
      case 'typescript': case 'javascript': return new TsWorker()
      default: return new EditorWorker()
    }
  },
}

loader.config({ monaco })

export { monaco }
