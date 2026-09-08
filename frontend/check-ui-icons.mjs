import { readdirSync, readFileSync } from 'node:fs'
import { dirname, extname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const frontend = dirname(fileURLToPath(import.meta.url))
const allowedSymbols = new Set(['©', '®', '™'])
const namedArrows = {
  larr: '←', leftarrow: '←', LeftArrow: '←',
  uarr: '↑', uparrow: '↑', UpArrow: '↑',
  rarr: '→', rightarrow: '→', RightArrow: '→',
  darr: '↓', downarrow: '↓', DownArrow: '↓',
  harr: '↔', leftrightarrow: '↔', LeftRightArrow: '↔',
  varr: '↕', updownarrow: '↕', UpDownArrow: '↕',
  nwarr: '↖', nwarrow: '↖', UpperLeftArrow: '↖',
  nearr: '↗', nearrow: '↗', UpperRightArrow: '↗',
  searr: '↘', searrow: '↘', LowerRightArrow: '↘',
  swarr: '↙', swarrow: '↙', LowerLeftArrow: '↙',
  lArr: '⇐', Leftarrow: '⇐', DoubleLeftArrow: '⇐',
  uArr: '⇑', Uparrow: '⇑', DoubleUpArrow: '⇑',
  rArr: '⇒', Rightarrow: '⇒', DoubleRightArrow: '⇒',
  dArr: '⇓', Downarrow: '⇓', DoubleDownArrow: '⇓',
  hArr: '⇔', Leftrightarrow: '⇔', DoubleLeftRightArrow: '⇔',
  vArr: '⇕', Updownarrow: '⇕', DoubleUpDownArrow: '⇕',
}

export function findIconViolations(source) {
  const decoded = source
    .replace(/\\u\{([\da-f]+)\}|\\u([\da-f]{4})/gi, (match, codePoint, codeUnit) => {
      const value = parseInt(codePoint || codeUnit, 16)
      return value <= 0x10ffff ? String.fromCodePoint(value) : match
    })
    .replace(/&#(x[\da-f]+|\d+);/gi, (match, value) => {
      const point = value[0].toLowerCase() === 'x' ? parseInt(value.slice(1), 16) : Number(value)
      return point <= 0x10ffff ? String.fromCodePoint(point) : match
    })
    .replace(/&([a-z]+);/gi, (match, name) => Object.hasOwn(namedArrows, name) ? namedArrows[name] : match)
    .replace(/&times;/g, '×')
    .replace(/\bcontent\s*:\s*(["'])([\s\S]*?)\1/gi, declaration =>
      declaration.replace(/\\([\da-f]{1,6})[ \t]?/gi, (match, hex) => {
        const point = parseInt(hex, 16)
        return point <= 0x10ffff ? String.fromCodePoint(point) : match
      }),
    )
  const violations = decoded.split('\n').flatMap((line, index) => {
    return [...line.matchAll(/[\p{Extended_Pictographic}✓]/gu)]
      .filter(([symbol]) => !allowedSymbols.has(symbol))
      .map(([symbol]) => ({ line: index + 1, symbol }))
  })
  const arrowContexts = [
    ...decoded.matchAll(/<(button|a|router-link|routerlink)\b[^>]*>[\s\S]*?<\/\1\s*>/gi),
    ...decoded.matchAll(/\bcontent\s*:\s*(["'])([\s\S]*?)\1/gi),
  ]
  for (const context of arrowContexts) {
    const text = context[0].replace(/<[^>]*>/g, tag => tag.replace(/[^\n]/g, ' '))
    for (const icon of text.matchAll(/[\u2190-\u21ff\u2794-\u27bf]/g)) {
      if (/\p{Extended_Pictographic}/u.test(icon[0])) continue
      const offset = context.index + icon.index
      const line = decoded.slice(0, offset).split('\n').length
      violations.push({ line, symbol: icon[0] })
    }
  }
  for (const button of decoded.matchAll(/<button\b[^>]*>[\s\S]*?<\/button\s*>/gi)) {
    for (const icon of button[0].matchAll(/>\s*×\s*</g)) {
      const offset = button.index + icon.index + icon[0].indexOf('×')
      violations.push({ line: decoded.slice(0, offset).split('\n').length, symbol: '×' })
    }
  }
  for (const interpolation of decoded.matchAll(/\{\{\s*(?:[\w$]+(?:\?\.|\.))+icon\b[^{}]*\}\}/g)) {
    violations.push({ line: decoded.slice(0, interpolation.index).split('\n').length, symbol: 'dynamic icon text' })
  }
  return violations
}

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    return /\.(?:test|spec)\./.test(entry.name) || !['.vue', '.ts', '.js', '.css', '.html', '.json'].includes(extname(path)) ? [] : [path]
  })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const files = ['user/src', 'admin/src'].flatMap(directory => sourceFiles(join(frontend, directory)))
  let count = 0
  for (const file of files) {
    for (const violation of findIconViolations(readFileSync(file, 'utf8'))) {
      console.error(`${relative(frontend, file)}:${violation.line}: ${violation.symbol} — use an SVG icon component or an image instead of icon text`)
      count++
    }
  }
  if (count) process.exitCode = 1
  else console.log(`UI icon check passed (${files.length} frontend source files).`)
}
