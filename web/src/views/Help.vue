<template>
  <div class="stack">
    <div class="card">
      <div class="card-head">
        <h2>{{ tt("help.gettingStarted") }}</h2>
        <!-- The version chip is platform information — admins only. -->
        <span v-if="admin" class="hint">v{{ version }}</span>
      </div>
      <p class="hint">{{ tt("help.intro") }}</p>
      <div class="grid two">
        <section class="col">
          <div class="card inner">
            <div class="card-head"><h2>{{ tt("help.forAllUsers") }}</h2></div>
            <ol>
              <li v-for="(x, i) in userBasics" :key="i">{{ x }}</li>
            </ol>
          </div>
          <div class="card inner">
            <div class="card-head"><h2>{{ tt("help.tipsAccount") }}</h2></div>
            <ul>
              <li v-for="(x, i) in tips" :key="i">{{ x }}</li>
            </ul>
          </div>
        </section>
        <section class="col">
          <div class="card inner">
            <div class="card-head"><h2>{{ tt("help.troubleshooting") }}</h2></div>
            <ul>
              <li v-for="(x, i) in troubleshooting" :key="i">{{ x }}</li>
            </ul>
          </div>
          <div class="card inner">
            <div class="card-head"><h2>{{ admin ? tt("help.adminGuide") : tt("help.needAdminHelp") }}</h2></div>
            <ol v-if="admin">
              <li v-for="(x, i) in adminBasics" :key="i">{{ x }}</li>
            </ol>
            <p v-else class="hint">{{ tt("help.needAdminHint") }}</p>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<script>
import { store, isAdmin, canMutate } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "Help",
  computed: {
    admin() { return isAdmin(); },
    version() { return store.me?.version || "dev"; },
    userBasics() {
      return [tt("help.b1"), tt("help.b2"), tt("help.b3"), tt("help.b4"), tt("help.b5"), tt("help.b6")];
    },
    adminBasics() {
      return [tt("help.a1"), tt("help.a2"), tt("help.a3"), tt("help.a4")];
    },
    troubleshooting() {
      return [tt("help.t1"), tt("help.t2"), tt("help.t3"), tt("help.t4")];
    },
    tips() {
      return [
        canMutate() ? tt("help.tipMutable") : tt("help.tipReadonly"),
        this.admin ? tt("help.tipAdmin") : tt("help.tipNonAdmin"),
      ];
    },
  },
  methods: {
    tt,
  },
};
</script>

<style scoped>
.stack > * + * { margin-top: 16px; }
.grid.two { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; }
@media (max-width: 800px) { .grid.two { grid-template-columns: minmax(0, 1fr); } }
.col { display: flex; flex-direction: column; gap: 16px; }
.card.inner { margin-bottom: 0; box-shadow: none; background: var(--fill); }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
ol, ul { margin: 0; padding-left: 20px; color: var(--muted); font-size: 13px; line-height: 1.9; }
</style>
