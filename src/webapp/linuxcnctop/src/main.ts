import { createApp } from 'vue';
import App from './App.vue';
import { statusStore } from './stores/status';

createApp(App).mount('#app');

// Auto-connect on load
statusStore.connect();
