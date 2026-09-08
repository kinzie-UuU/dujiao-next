import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { findIconViolations } from './check-ui-icons.mjs'

test('rejects emoji fallbacks, emoji-capable arrows and character checkmarks', () => {
  assert.deepEqual(findIconViolations('<span>👤 ⭐ ↗ ✓</span>').map(item => item.symbol), ['👤', '⭐', '↗', '✓'])
})

test('decodes HTML entities, Unicode escapes and surrogate pairs', () => {
  const source = String.raw`&#10003; &#x2197; \u{1F464} \uD83D\uDC64`
  assert.deepEqual(findIconViolations(source).map(item => item.symbol), ['✓', '↗', '👤', '👤'])
})

test('preserves ordinary text arrows, country information and legal marks', () => {
  assert.deepEqual(findIconViolations('设置 → 安全；金额 ↓；🇨🇳 中国；© ® ™'), [])
  assert.deepEqual(findIconViolations('<p>设置 &rarr; 安全</p><SelectItem>金额 ↓</SelectItem>'), [])
})

test('rejects plain arrows in button and link text, including nested and multiline labels', () => {
  assert.deepEqual(findIconViolations('<button>下一页 →</button>'), [{ line: 1, symbol: '→' }])
  assert.deepEqual(findIconViolations('<a href="/">\n返回 <span>←</span>\n</a>'), [{ line: 2, symbol: '←' }])
  assert.deepEqual(findIconViolations('<Button>\n<span>继续</span>\n<span>➜</span>\n</Button>'), [{ line: 3, symbol: '➜' }])
  assert.deepEqual(findIconViolations('<router-link to="/">返回 ←</router-link>'), [{ line: 1, symbol: '←' }])
  assert.deepEqual(findIconViolations('<RouterLink to="/">下一页 →</RouterLink>'), [{ line: 1, symbol: '→' }])
  assert.deepEqual(findIconViolations('<button title="设置 → 安全">设置<ArrowRight /></button>'), [])
})

test('decodes named arrow entities and escaped plain arrows in controls', () => {
  assert.deepEqual(findIconViolations('<button>&rarr; &RightArrow; &rArr;</button>').map(item => item.symbol), ['→', '→', '⇒'])
  assert.deepEqual(findIconViolations('<a>&nearr;</a>'), [{ line: 1, symbol: '↗' }])
  assert.deepEqual(findIconViolations(String.raw`<button>\u2192 &#8592; &#x2191;</button>`).map(item => item.symbol), ['→', '←', '↑'])
})

test('decodes CSS content escapes and rejects plain generated arrows without losing line numbers', () => {
  assert.deepEqual(findIconViolations(String.raw`.next::after { content: '\2197'; }`), [{ line: 1, symbol: '↗' }])
  assert.deepEqual(findIconViolations(String.raw`.next::after { content: '\002192 '; }`), [{ line: 1, symbol: '→' }])
  assert.deepEqual(findIconViolations(".next::after {\n  content: '\\2192';\n}"), [{ line: 2, symbol: '→' }])
  assert.deepEqual(findIconViolations(String.raw`.label::after { content: '\00a9'; }`), [])
})

test('reports the original source line and leaves out-of-range escapes alone', () => {
  assert.deepEqual(findIconViolations('normal\n&#10003;\n\\u{FFFFFF}'), [{ line: 2, symbol: '✓' }])
})

test('rejects a standalone close character in buttons while preserving multiplication', () => {
  assert.deepEqual(findIconViolations('<button>\n<span>item</span><span>&times;</span>\n</button>'), [{ line: 2, symbol: '×' }])
  assert.deepEqual(findIconViolations('<button>&#215;</button>'), [{ line: 1, symbol: '×' }])
  assert.deepEqual(findIconViolations('<p>单价 × 数量</p><button>计算 2 × 3</button>'), [])
})

test('rejects configured icon text interpolation while allowing images and SVG fallbacks', () => {
  assert.deepEqual(findIconViolations('<span>{{ userProfileStore.currentLevel.icon }}</span>'), [{ line: 1, symbol: 'dynamic icon text' }])
  assert.deepEqual(findIconViolations('<span>{{ level.icon }}</span>'), [{ line: 1, symbol: 'dynamic icon text' }])
  assert.deepEqual(findIconViolations('<span>{{ userProfileStore.currentLevel?.icon }}</span>'), [{ line: 1, symbol: 'dynamic icon text' }])
  assert.deepEqual(findIconViolations('<img v-if="isImagePath(level.icon)" :src="getImageUrl(level.icon)" /><UserRound v-else />'), [])
  assert.deepEqual(findIconViolations("{{ t('admin.memberLevels.table.icon') }}"), [])
})

test('member level settings cannot offer a text or emoji icon editor', () => {
  const source = readFileSync(new URL('./admin/src/views/admin/MemberLevels.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /\bEmoji\b|switchIconMode|<Input\b[^>]*v-model="form\.icon"/)
})
