<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from '@/i18n'
import { useAppendList } from '@/rows'
import type { Account } from '@/types'
import FilterMenu from './FilterMenu.vue'
import UiIcon from './UiIcon.vue'

const props = defineProps<{ accounts: Account[]; value: string }>()
const emit = defineEmits<{ change: [value: string] }>()

const { t } = useI18n()
// dragging 与 over 是拖动中的账户序号和指针所在的账户序号
const dragging = ref(-1)
const over = ref(-1)

// label 返回账户 ID 对应的显示名称
function label(id: string): string {
  return props.accounts.find((account) => account.id === id)?.label ?? id
}

// sortedAccounts 是按稳定 ID 升序排列的账户基准列表（对应 1/2/3/4 与 #1/#2 序号）
const sortedAccounts = computed(() =>
  [...props.accounts].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)),
)

// resolveToken 将单个账户简写（精确 ID、#N、唯一标签、数字序号）解析为账户 ID，无法解析（如 @规则名）时返回 null
function resolveToken(raw: string): string | null {
  const token = raw.trim()
  if (!token || token.startsWith('@')) return null
  const exact = props.accounts.find((a) => a.id === token)
  if (exact) return exact.id
  const base = sortedAccounts.value
  if (token.startsWith('#')) {
    const idx = Number(token.slice(1).trim())
    return Number.isInteger(idx) && idx >= 1 && idx <= base.length ? base[idx - 1]!.id : null
  }
  const ci = base.find((a) => a.id.toLowerCase() === token.toLowerCase())
  if (ci) return ci.id
  const byLabel = base.filter((a) => a.label.trim().toLowerCase() === token.toLowerCase())
  if (byLabel.length === 1) return byLabel[0]!.id
  const num = Number(token)
  if (Number.isInteger(num) && num >= 1 && num <= base.length) return base[num - 1]!.id
  return null
}

const rawTokens = computed(() =>
  props.value
    .split(/[,|>/\s]+/)
    .map((s) => s.trim())
    .filter(Boolean),
)

// isCustomExpression 表示当前值包含命名规则引用（如 @rev）等无法映射为静态账户列表的表达式，此时展示文本框防止误清空
const isCustomExpression = computed(() =>
  rawTokens.value.some((token) => resolveToken(token) === null),
)

// priority 是配置中列出且仍然存在的优先账户，按配置顺序
const priority = computed(() => {
  if (isCustomExpression.value) return []
  const resolved: string[] = []
  for (const token of rawTokens.value) {
    const id = resolveToken(token)
    if (id && !resolved.includes(id)) resolved.push(id)
  }
  return resolved
})

// rest 是未列出的账户数，这些账户按 ID 排在优先账户之后
const rest = computed(() => props.accounts.length - priority.value.length)

// write 写回优先账户顺序
function write(ids: string[]): void {
  emit('change', ids.join(','))
}

// select 按菜单的选择更新优先账户，已列出的保持原位，新选的追加到末尾
function select(ids: string[]): void {
  write([
    ...priority.value.filter((id) => ids.includes(id)),
    ...ids.filter((id) => !priority.value.includes(id)),
  ])
}

// move 把序号 from 的账户移到序号 to
function move(from: number, to: number): void {
  const ids = [...priority.value]
  ids.splice(to, 0, ...ids.splice(from, 1))
  write(ids)
}

// drop 把拖动中的账户放到指针所在的位置
function drop(index: number): void {
  if (dragging.value !== -1 && dragging.value !== index) move(dragging.value, index)
  endDrag()
}

const reveal = useAppendList('list')
// 新加入的账户排在末尾，加入后把它滚入列表
watch(
  () => priority.value.length,
  (count, before) => count > before && reveal(false),
)

// endDrag 结束拖动
function endDrag(): void {
  dragging.value = -1
  over.value = -1
}
</script>

<template>
  <div class="space-y-2">
    <input
      v-if="isCustomExpression"
      :value="value"
      type="text"
      class="w-full rounded-md border border-line bg-canvas px-3 py-2 text-sm text-white"
      @input="emit('change', ($event.target as HTMLInputElement).value)"
    />
    <template v-else>
      <ol
        v-if="priority.length > 0"
        ref="list"
        class="append-list"
        aria-labelledby="setting-account-order-label"
      >
        <li
          v-for="(id, index) in priority"
          :key="id"
          class="group/item flex items-center gap-1.5 rounded border-t py-1 pr-1 transition-colors hover:bg-raised"
          :class="over === index && dragging !== index ? 'border-gray-400' : 'border-transparent'"
          @dragover.prevent="over = index"
          @dragleave="over = -1"
          @drop.prevent="drop(index)"
        >
          <span
            draggable="true"
            class="cursor-grab text-gray-600 opacity-0 group-hover/item:opacity-100 hover:text-gray-300 active:cursor-grabbing [@media(hover:none)]:opacity-100"
            :aria-label="t('settings.accountOrderDrag').replace('{account}', label(id))"
            @dragstart="dragging = index"
            @dragend="endDrag"
          >
            <UiIcon name="grip" :size="14" />
          </span>
          <span class="w-4 shrink-0 text-right text-xs text-gray-500 tabular-nums">{{
            index + 1
          }}</span>
          <span
            v-tooltip.overflow="label(id)"
            class="min-w-0 flex-1 truncate text-sm text-gray-200"
            >{{ label(id) }}</span
          >
          <button
            type="button"
            class="rounded p-0.5 text-gray-500 opacity-0 transition group-hover/item:opacity-100 hover:text-gray-200 focus-visible:opacity-100 disabled:!opacity-0 [@media(hover:none)]:opacity-100"
            :aria-label="t('settings.accountOrderUp').replace('{account}', label(id))"
            :disabled="index === 0"
            @click="move(index, index - 1)"
          >
            <UiIcon name="chevronUp" :size="14" />
          </button>
          <button
            type="button"
            class="rounded p-0.5 text-gray-500 opacity-0 transition group-hover/item:opacity-100 hover:text-gray-200 focus-visible:opacity-100 disabled:!opacity-0 [@media(hover:none)]:opacity-100"
            :aria-label="t('settings.accountOrderDown').replace('{account}', label(id))"
            :disabled="index === priority.length - 1"
            @click="move(index, index + 1)"
          >
            <UiIcon name="chevronDown" :size="14" />
          </button>
          <button
            v-tooltip="t('settings.accountOrderRemove').replace('{account}', label(id))"
            type="button"
            class="rounded p-0.5 text-gray-500 opacity-0 transition group-hover/item:opacity-100 hover:text-gray-200 focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
            :aria-label="t('settings.accountOrderRemove').replace('{account}', label(id))"
            @click="write(priority.filter((item) => item !== id))"
          >
            <UiIcon name="close" :size="14" />
          </button>
        </li>
      </ol>
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <FilterMenu
          field
          class="w-full rounded-md border border-line bg-canvas px-3 py-2 text-sm text-white"
          :label="t('settings.accountOrderAdd')"
          :options="accounts.map((account) => account.id)"
          :selected="priority"
          :format="label"
          :search-label="t('settings.accountSearch')"
          :empty-label="t('settings.accountNone')"
          :clear-label="t('settings.accountOrderClear')"
          @change="select"
        />
        <span class="text-xs text-gray-500">{{
          priority.length === 0
            ? t('settings.accountOrderUnset')
            : t('settings.accountOrderRest').replace('{count}', String(rest))
        }}</span>
      </div>
    </template>
  </div>
</template>
