<template>
  <div class="filelist">
    <table>
      <thead>
        <tr>
          <th style="width:30px"><input type="checkbox" :checked="files.length > 0 && selected.size === files.length" @change="toggleAll" /></th>
          <th>ชื่อไฟล์</th><th>ขนาด</th><th>แก้ไขล่าสุด</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="f in files" :key="f.name">
          <td><input type="checkbox" :checked="selected.has(f.name)" @change="toggle(f.name)" /></td>
          <td>{{ f.name }}</td>
          <td class="muted mono">{{ fmtSize(f.size) }}</td>
          <td class="muted mono">{{ f.modTime }}</td>
        </tr>
        <tr v-if="!files.length"><td colspan="4" class="muted">(ไม่พบไฟล์)</td></tr>
      </tbody>
    </table>
  </div>
</template>

<script setup>
const props = defineProps({ files: { type: Array, default: () => [] }, selected: { type: Object, required: true } })
const emit = defineEmits(['update:selected'])
const toggle = (n) => {
  const s = new Set(props.selected)
  s.has(n) ? s.delete(n) : s.add(n)
  emit('update:selected', s)
}
const toggleAll = () => {
  emit('update:selected', props.selected.size === props.files.length ? new Set() : new Set(props.files.map(f => f.name)))
}
</script>
