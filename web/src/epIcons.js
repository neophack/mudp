// Explicitly registered Element Plus icons. Views reference icons by string
// name ("prefix-icon=\"Search\"", rowActions' icon: \"VideoPlay\", ActionSheet's
// <component :is>), which resolves through globally registered components —
// but only the names below are referenced anywhere in src/ (verified by
// scanning every quoted string against the package's exports). Registering
// all 293 icons would ship ~200KB of unused SVG components.
// When adding an icon to the UI, add its name here too.
import {
  Avatar,
  CircleCheck,
  Close,
  CopyDocument,
  DataLine,
  Delete,
  Document,
  Download,
  Edit,
  Failed,
  FolderOpened,
  Help,
  InfoFilled,
  Key,
  Link,
  Monitor,
  Rank,
  Refresh,
  Search,
  Setting,
  Share,
  SwitchButton,
  VideoPause,
  VideoPlay,
} from "@element-plus/icons-vue";

export const epIcons = {
  Avatar,
  CircleCheck,
  Close,
  CopyDocument,
  DataLine,
  Delete,
  Document,
  Download,
  Edit,
  Failed,
  FolderOpened,
  Help,
  InfoFilled,
  Key,
  Link,
  Monitor,
  Rank,
  Refresh,
  Search,
  Setting,
  Share,
  SwitchButton,
  VideoPause,
  VideoPlay,
};
