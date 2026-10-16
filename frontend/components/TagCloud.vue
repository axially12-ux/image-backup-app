<template>
  <div class="card">
    <h3>🏷 Tag Cloud <span class="muted" style="font-weight:400">(คลิก Tag  เพื่อค้นหาภาพ)</span></h3>
    <div class="cloud" v-if="tags.length">
      <button v-for="t in tags" :key="t.name" :class="{ sel: t.name === selected }"
              :style="{ fontSize: size(t.count) + 'px', opacity: 0.6 + 0.4 * weight(t.count) }"
              :title="t.count + ' รูป'" @click="$emit('pick', t.name)">
        {{ t.name }}<sup class="muted"> {{ t.count }}</sup>
      </button>
    </div>
    <div v-else class="muted">ยังไม่มี Tag — Backup รูปก่อน แล้วระบบจะวิเคราะห์และสร้าง Tag ให้</div>
  </div>
</template>

<script setup>
const props = defineProps({ tags: { type: Array, default: () => [] }, selected: { type: String, default: '' } })
defineEmits(['pick'])

// ขนาดตัวอักษรตามจำนวนรูป (ใช้ log เพื่อไม่ให้ tag ที่มีมากสุดกลบทั้งหมด) 12–34 px
const maxC = computed(() => Math.max(1, ...props.tags.map(t => t.count)))
const weight = (c) => (maxC.value <= 1 ? 0.5 : Math.log(c + 1) / Math.log(maxC.value + 1))
const size = (c) => Math.round(12 + weight(c) * 22)
</script>
