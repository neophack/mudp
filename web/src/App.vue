<template>
  <el-config-provider :locale="elementLocale" size="small">
    <div id="mudp-app">
      <router-view />
      <copy-overlay />
      <upload-overlay />
      <upgrade-dialog />
      <upgrade-overlay />
    </div>
  </el-config-provider>
</template>

<script>
import { elementLocale } from "@/i18n";
import CopyOverlay from "@/components/CopyOverlay.vue";
import UploadOverlay from "@/components/UploadOverlay.vue";
import UpgradeDialog from "@/components/UpgradeDialog.vue";
import UpgradeOverlay from "@/components/UpgradeOverlay.vue";

export default {
  name: "App",
  components: { CopyOverlay, UploadOverlay, UpgradeDialog, UpgradeOverlay },
  data() {
    return { elementLocale };
  },
};
</script>
