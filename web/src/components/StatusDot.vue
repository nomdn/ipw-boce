<template>
  <span class="dot-s" :class="{ on: on === true, err: err }" :title="title" />
</template>

<script setup>
defineProps({ on: Boolean, err: Boolean, title: String })
</script>
