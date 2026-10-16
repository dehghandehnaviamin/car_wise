<script setup lang="ts">
import { computed, ref } from 'vue'

type Listing = { id:number; brand:string; model:string; trim:string; year:number; price_irr:number; mileage_km:number; color:string; city:string; transmission:string; fuel_consumption_l100km:number; body_condition:string; features:string[]; image_url:string; source:string; source_url:string; description:string; ranking_score?:number; reasons?:string[]; cautions?:string[] }
type CompareRecommendation = { id:number; title:string; fit:string; tradeoffs?:string[]; best_for:string }
const api = import.meta.env.VITE_API_URL || ''
const query = ref('')
const assistantMode = ref(true)
const loading = ref(false)
const listings = ref<Listing[]>([])
const assistantMessage = ref('')
const selected = ref<number[]>([])
const intent = ref<any>(null)
const knowledge = ref<string[]>([])
const tradeoffs = ref<string[]>([])
const page = ref(1)
const total = ref(0)
const compareLoading = ref(false)
const compareError = ref('')
const compareResult = ref<{ items:Listing[]; recommendations:CompareRecommendation[]; overall_recommendation:string } | null>(null)
const selectionNotice = ref('')
const listOrEmpty = <T>(value: unknown): T[] => Array.isArray(value) ? value as T[] : []
const criterionLabels: Record<string, string> = { low_fuel:'کم‌مصرف', high_fuel:'پرمصرف', comfort:'نرم و راحت', economic:'اقتصادی', luxury:'لوکس', options:'آپشن‌دار', compact:'کوچک و شهری', city_automatic:'اتومات مناسب شهر', chinese:'چینی', iranian:'ایرانی', foreign:'خارجی', korean:'کره‌ای', japanese:'ژاپنی', german:'آلمانی', sedan:'سدان', hatchback:'هاچ‌بک', coupe:'کوپه', suv:'SUV / شاسی‌بلند', pickup:'پیکاپ', no_paint:'بی‌رنگ', painted:'رنگ‌دار', repaired:'صافکاری/نیم‌رنگ', accident:'تصادفی', automatic:'اتومات', manual:'دنده‌ای', optional:'آپشنال', full:'فول', excellent:'اکسلنت', hybrid:'هیبرید', electric:'برقی' }
const labelCriterion = (value:string) => criterionLabels[value] || value

const formatPrice = (n:number) => `${Math.round(n / 10).toLocaleString('fa-IR')} تومان`
const selectedListings = computed(() => listings.value.filter(l => selected.value.includes(l.id)))
const safeURL = (value:string, allowImage = false) => {
  try {
    const url = new URL(value)
    if (url.protocol !== 'https:') return ''
    if (allowImage) return url.href
    const host = url.hostname.toLowerCase()
    return ['divar.ir','www.divar.ir','hamrah-mechanic.com','www.hamrah-mechanic.com','karnameh.com','www.karnameh.com','khodro45.com','www.khodro45.com'].some(h => host === h || host.endsWith(`.${h}`)) ? url.href : ''
  } catch { return '' }
}

async function search() {
  if (!query.value.trim()) return
  loading.value = true
  compareError.value = ''; compareResult.value = null; selectionNotice.value = ''; selected.value = []
  try {
    if (assistantMode.value) {
      const res = await fetch(`${api}/api/v1/assistant/search`, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({ message: query.value }) })
      const data = await res.json().catch(() => ({})); if (!res.ok) throw new Error(data?.message || 'جست‌وجوی هوشمند ناموفق بود')
      const results = data?.results || {}; listings.value = listOrEmpty<Listing>(results.items); intent.value = data?.intent || null; knowledge.value = listOrEmpty<string>(results.knowledge); tradeoffs.value = listOrEmpty<string>(data?.tradeoffs); assistantMessage.value = data?.message || ''; total.value = Number(data?.results?.total ?? listings.value.length) || 0
    } else {
      const res = await fetch(`${api}/api/v1/listings?query=${encodeURIComponent(query.value)}&page=${page.value}`); const data = await res.json().catch(() => ({})); if (!res.ok) throw new Error(data?.message || 'جست‌وجو ناموفق بود'); listings.value = listOrEmpty<Listing>(data?.items); total.value = Number(data?.total) || 0; intent.value = null; knowledge.value = []; assistantMessage.value = ''
    }
  } catch (error) {
    listings.value = []; assistantMessage.value = error instanceof Error ? error.message : 'خطایی در جست‌وجو رخ داد'
  } finally { loading.value = false }
}
function toggle(id:number) {
  selectionNotice.value = ''
  selected.value = selected.value.includes(id) ? selected.value.filter(x=>x!==id) : selected.value.length >= 3 ? (selectionNotice.value = 'برای مقایسه هم‌زمان حداکثر ۳ آگهی را انتخاب کن', selected.value) : [...selected.value,id]
}
async function compareSelected() {
  if (selected.value.length < 2) { selectionNotice.value = 'برای مقایسه حداقل ۲ آگهی را انتخاب کن'; return }
  compareLoading.value = true; compareError.value = ''; compareResult.value = null
  try {
    const res = await fetch(`${api}/api/v1/compare`, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({ ids: selected.value }) })
    const data = await res.json().catch(() => ({})); if (!res.ok) throw new Error(data?.message || 'مقایسه ناموفق بود')
    compareResult.value = { items: listOrEmpty<Listing>(data?.items), recommendations: listOrEmpty<CompareRecommendation>(data?.recommendations), overall_recommendation: String(data?.overall_recommendation || '') }
  } catch (error) { compareError.value = error instanceof Error ? error.message : 'مقایسه ناموفق بود' } finally { compareLoading.value = false }
}
function reset() { query.value=''; listings.value=[]; assistantMessage.value=''; intent.value=null; knowledge.value=[]; tradeoffs.value=[]; selected.value=[]; compareResult.value=null; compareError.value=''; selectionNotice.value='' }
</script>

<template>
  <main class="shell">
    <header class="topbar"><div class="brand"><span class="brand-mark">ک</span><div><strong>کاروایز</strong><small>دستیار خرید خودروی کارکرده</small></div></div><div class="profile">◉</div></header>
    <section class="hero"><h1>خودروی مناسبت رو با <span>توصیف معیارهات</span> پیدا کن.</h1><p class="sub">آگهی‌ها رو بر اساس بودجه، نیاز و اولویت‌های خودت بررسی کن.</p>
      <div class="search-wrap" :class="{ expanded: assistantMode, smart: assistantMode }"><button class="mode-btn" :class="{ active: assistantMode }" @click="assistantMode=!assistantMode" :title="assistantMode ? 'حالت جست‌وجوی عادی' : 'دستیار هوشمند'">✦</button><input v-model="query" @keyup.enter="search" :placeholder="assistantMode ? 'مثلاً: برای خانواده پرجمعیت، جادار و لوکس...' : 'مدل، بودجه یا ویژگی مورد نظر را جست‌وجو کن...'"/><div class="search-action"><div v-if="assistantMode" class="assistant-tip">با توصیف خواسته‌هات و مکالمه با دستیار هوشمند می‌تونی آگهی‌های مناسبتو بهتر پیدا کنی</div><button class="search-btn" :class="{ 'smart-btn': assistantMode }" @click="search">{{ loading ? '...' : assistantMode ? 'جست‌وجوی هوشمند' : 'جست‌وجو' }}</button></div></div>
      <div class="chips"><button @click="query='ماشین کم‌مصرف مناسب شهر'; assistantMode=true">کم‌مصرف و شهری</button><button @click="query='ماشین پر آپشن بین ۲ تا ۴ میلیارد بدون رنگ'; assistantMode=true">پرآپشن و بدون رنگ</button><button @click="query='ماشین جادار و لوکس برای خانواده'; assistantMode=true">جادار و خانوادگی</button></div>
    </section>

    <section v-if="assistantMessage" class="assistant-card"><div class="spark">✦</div><div><strong>{{ assistantMessage }}</strong><div v-if="intent" class="intent-row"><span v-for="p in (intent.priorities || [])" :key="p">{{ labelCriterion(p) }}</span><span v-for="m in (intent.models || [])" :key="m">{{ m }}</span><span v-for="o in (intent.origins || [])" :key="o">{{ labelCriterion(o) }}</span><span v-for="c in (intent.colors || [])" :key="c">{{ c }}</span><span v-for="b in (intent.body_types || [])" :key="b">{{ labelCriterion(b) }}</span><span v-for="b in (intent.body_conditions || [])" :key="b">{{ labelCriterion(b) }}</span><span v-for="e in (intent.equipment_levels || [])" :key="e">{{ labelCriterion(e) }}</span><span v-for="p in (intent.powertrains || [])" :key="p">{{ labelCriterion(p) }}</span><span v-if="intent.transmission">{{ labelCriterion(intent.transmission) }}</span><span v-if="intent.min_year || intent.max_year">مدل {{ intent.min_year || 'اول' }} تا {{ intent.max_year || 'جدیدترین' }}</span><span v-if="intent.max_mileage_km">کارکرد تا {{ intent.max_mileage_km.toLocaleString('fa-IR') }} کیلومتر</span><span v-if="intent.max_price_irr">تا {{ formatPrice(intent.max_price_irr) }}</span></div><div v-if="tradeoffs.length" class="tradeoffs"><p v-for="item in tradeoffs" :key="item">{{ item }}</p></div></div></section>
    <section v-if="knowledge.length" class="knowledge"><strong>داده‌های پشتیبان پاسخ</strong><p v-for="k in knowledge" :key="k">{{ k }}</p></section>

    <section v-if="listings.length" class="results"><div class="section-head"><div><p class="eyebrow">{{ assistantMode ? 'RANKED RECOMMENDATIONS' : 'SEARCH RESULTS' }}</p><h2>{{ total.toLocaleString('fa-IR') }} آگهی پیدا شد</h2></div><div v-if="selected.length" class="compare-actions"><div class="compare-pill">{{ selected.length }} انتخاب <button @click="selected=[]">×</button></div><button class="compare-btn" :disabled="compareLoading || selected.length < 2" @click="compareSelected">{{ compareLoading ? 'در حال تحلیل…' : 'مقایسه و پیشنهاد' }}</button></div></div><p v-if="selectionNotice" class="selection-notice">{{ selectionNotice }}</p><div class="grid"><article v-for="car in listings" :key="car.id" class="car-card"><div class="photo-wrap"><img v-if="safeURL(car.image_url, true)" :src="safeURL(car.image_url, true)" :alt="`${car.brand} ${car.model}`"/><span class="source">{{ car.source }}</span><button class="check" :class="{ checked:selected.includes(car.id) }" @click="toggle(car.id)">{{ selected.includes(car.id) ? '✓' : '+' }}</button></div><div class="card-body"><div class="title-row"><div><h3>{{ car.brand }} {{ car.model }}</h3><p>{{ car.trim }} · {{ car.year }}</p></div><strong class="price">{{ formatPrice(car.price_irr) }}</strong></div><div class="metrics"><span>{{ car.mileage_km.toLocaleString('fa-IR') }} km</span><span>{{ car.transmission }}</span><span>{{ car.color }}</span></div><div class="why"><span>✦</span>{{ assistantMode ? (car.reasons?.slice(0, 2).join(' · ') || car.description) : 'اطلاعات نرمال‌شده از منبع اصلی' }}</div><div v-if="assistantMode && car.cautions?.length" class="caution">{{ car.cautions[0] }}</div><a v-if="safeURL(car.source_url)" :href="safeURL(car.source_url)" target="_blank" rel="noopener noreferrer">مشاهده آگهی اصلی ↗</a></div></article></div></section>
    <section v-if="compareResult" class="compare-panel"><div class="section-head"><div><p class="eyebrow">SMART COMPARISON</p><h2>تحلیل سه گزینه</h2></div><button class="compare-close" @click="compareResult=null">×</button></div><p class="compare-overall">{{ compareResult.overall_recommendation }}</p><div class="compare-grid"><article v-for="recommendation in compareResult.recommendations" :key="recommendation.id" class="recommendation-card"><strong>{{ recommendation.title }}</strong><p>{{ recommendation.fit }}</p><small>مناسب برای: {{ recommendation.best_for }}</small><ul><li v-for="tradeoff in recommendation.tradeoffs || []" :key="tradeoff">{{ tradeoff }}</li></ul></article></div></section><p v-if="compareError" class="compare-error">{{ compareError }}</p>
    <section v-if="!listings.length" class="empty"><div class="empty-icon">⌁</div><h2>{{ query ? 'نتیجه‌ای مطابق همه معیارها پیدا نشد' : 'از کجا شروع کنیم؟' }}</h2><p>{{ query ? 'رنگ، بودجه یا معیار دیگری را کمی بازتر کن و دوباره جست‌وجو کن.' : 'یک عبارت جست‌وجو وارد کن یا آیکون ✦ را برای گفت‌وگو با دستیار بزن.' }}</p></section>
    <footer>کاروایز · دستیار خرید خودروی کارکرده</footer>
  </main>
</template>
