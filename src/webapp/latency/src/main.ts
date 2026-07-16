import { createApp } from 'vue';
import App from './App.vue';
import { latencyStore } from './stores/latency';

createApp(App).mount('#app');
latencyStore.start();
