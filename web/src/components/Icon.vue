<template>
  <component :is="comp" :size="size" :stroke-width="stroke" aria-hidden="true" />
</template>

<script>
// 统一图标入口：<Icon name="Bell" />。只登记用到的图标，避免把整个 lucide 打进包
import {
  Activity, ArrowUp, Bell, Building2, CalendarCheck, Check, ChevronDown, ChevronRight, Circle, CircleAlert,
  CircleCheck, Coins, Copy, Download, Ellipsis, FileSearch, Gauge, Gift, Globe, HandHeart, House, KeyRound,
  LayoutDashboard, Lock, LogOut, MapPin, Monitor, Moon, Network, Package, PanelLeft, Pencil, Plus, Puzzle,
  RefreshCw, RotateCcw, ScrollText, Search, Server, Settings, Share2, ShieldCheck, ShoppingBag, Square, SquarePen,
  Sun, Terminal, Ticket, Trash2, User, Users, Wallet, X, Zap,
} from '@lucide/vue'

const REGISTRY = {
  Activity, ArrowUp, Bell, Building2, CalendarCheck, Check, ChevronDown, ChevronRight, Circle, CircleAlert,
  CircleCheck, Coins, Copy, Download, Ellipsis, FileSearch, Gauge, Gift, Globe, HandHeart, House, KeyRound,
  LayoutDashboard, Lock, LogOut, MapPin, Monitor, Moon, Network, Package, PanelLeft, Pencil, Plus, Puzzle,
  RefreshCw, RotateCcw, ScrollText, Search, Server, Settings, Share2, ShieldCheck, ShoppingBag, Square, SquarePen,
  Sun, Terminal, Ticket, Trash2, User, Users, Wallet, X, Zap,
}

export default {
  props: {
    name: { type: String, required: true },
    size: { type: [Number, String], default: 18 },
    stroke: { type: [Number, String], default: 1.75 },
  },
  computed: {
    comp() {
      return REGISTRY[this.name] || Circle
    },
  },
}
</script>
