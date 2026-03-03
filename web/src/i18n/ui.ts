export const ui = {
  en: {
    'nav.home': 'Home',
    'nav.about': 'About',
    'nav.docs': 'Documentation',
    'nav.github': 'GitHub',
    'hero.title': 'NEURAL INTERFACE ONLINE',
    'hero.subtitle': 'Advanced system monitoring for the next generation. Secure, blazing fast, and effortlessly sexy.',
    'hero.cta': 'INITIALIZE SYSTEM',
    'features.title': 'SYSTEM CAPABILITIES',
    'feature.1.title': 'Real-time Metrics',
    'feature.1.desc': 'Sub-millisecond data telemetry synchronized directly to your optical nerves.',
    'feature.2.title': 'A.I. Defense',
    'feature.2.desc': 'Automated heuristics neutralizing threats before they even compile.',
    'feature.3.title': 'Global Grid',
    'feature.3.desc': 'Connected to 50+ nodes worldwide. Zero latency tolerance.',
    'footer.text': 'OpsView Corporation. All systems operational.'
  },
  ko: {
    'nav.home': '홈',
    'nav.about': '정보',
    'nav.docs': '문서',
    'nav.github': '깃허브',
    'hero.title': '신경망 인터페이스 접속',
    'hero.subtitle': '차세대를 위한 고급 시스템 모니터링. 안전하고, 눈부시게 빠르며, 완벽하게 섹시합니다.',
    'hero.cta': '시스템 초기화',
    'features.title': '시스템 기능',
    'feature.1.title': '실시간 메트릭',
    'feature.1.desc': '시신경에 직접 동기화되는 1ms 미만의 데이터 원격 측정.',
    'feature.2.title': 'A.I. 방어체계',
    'feature.2.desc': '위협이 컴파일 되기 전에 자동화된 휴리스틱 알고리즘으로 무력화.',
    'feature.3.title': '글로벌 그리드',
    'feature.3.desc': '전 세계 50개 이상의 노드 연결. 지연 시간 제로.',
    'footer.text': 'OpsView Corporation. 모든 시스템 정상 가동 중.'
  },
  ja: {
    'nav.home': 'ホーム',
    'nav.about': '概要',
    'nav.docs': 'ドキュメント',
    'nav.github': 'GitHub',
    'hero.title': '神経インターフェース オンライン',
    'hero.subtitle': '次世代向けの高度なシステム監視。安全で、驚くほど速く、圧倒的にセクシー。',
    'hero.cta': 'システム初期化',
    'features.title': 'システム機能',
    'feature.1.title': 'リアルタイムメトリクス',
    'feature.1.desc': '視神経に直接同期されるサブミリ秒のデータテレメトリ。',
    'feature.2.title': 'A.I. 防衛',
    'feature.2.desc': 'コンパイルされる前に脅威を無効化する自動ヒューリスティクス。',
    'feature.3.title': 'グローバルグリッド',
    'feature.3.desc': '世界中の50以上のノードに接続。遅延許容度ゼロ。',
    'footer.text': 'OpsView Corporation. 全システム稼働中。'
  },
  pt: {
    'nav.home': 'Início',
    'nav.about': 'Sobre',
    'nav.docs': 'Documentação',
    'nav.github': 'GitHub',
    'hero.title': 'INTERFACE NEURAL ONLINE',
    'hero.subtitle': 'Monitoramento avançado de sistemas para a próxima geração. Seguro, incrivelmente rápido e totalmente sexy.',
    'hero.cta': 'INICIALIZAR SISTEMA',
    'features.title': 'CAPACIDADES DO SISTEMA',
    'feature.1.title': 'Métricas em Tempo Real',
    'feature.1.desc': 'Telemetria de dados sub-milissegundos sincronizada diretamente aos seus nervos ópticos.',
    'feature.2.title': 'Defesa A.I.',
    'feature.2.desc': 'Heurística automatizada neutralizando ameaças antes mesmo de serem compiladas.',
    'feature.3.title': 'Grade Global',
    'feature.3.desc': 'Conectado a mais de 50 nós em todo o mundo. Tolerância zero a latência.',
    'footer.text': 'OpsView Corporation. Todos os sistemas operacionais.'
  },
  es: {
    'nav.home': 'Inicio',
    'nav.about': 'Acerca de',
    'nav.docs': 'Documentación',
    'nav.github': 'GitHub',
    'hero.title': 'INTERFAZ NEURAL ONLINE',
    'hero.subtitle': 'Monitorización avanzada de sistemas para la próxima generación. Seguro, increíblemente rápido y absolutamente sexy.',
    'hero.cta': 'INICIALIZAR SISTEMA',
    'features.title': 'CAPACIDADES DEL SISTEMA',
    'feature.1.title': 'Métricas en Tiempo Real',
    'feature.1.desc': 'Telemetría de datos sub-milisegundo sincronizada directamente a tus nervios ópticos.',
    'feature.2.title': 'Defensa I.A.',
    'feature.2.desc': 'Heurísticas automatizadas neutralizando amenazas antes de que se compilen.',
    'feature.3.title': 'Red Global',
    'feature.3.desc': 'Conectado a más de 50 nodos mundiales. Tolerancia cero a la latencia.',
    'footer.text': 'OpsView Corporation. Todos los sistemas operativos.'
  }
} as const;

export const languages = {
  en: 'English',
  ko: '한국어',
  ja: '日本語',
  pt: 'Português',
  es: 'Español',
};

export const defaultLang = 'en';

export function getLangFromUrl(url: URL) {
  const [, lang] = url.pathname.split('/');
  if (lang in ui) return lang as keyof typeof ui;
  return defaultLang;
}

export function useTranslations(lang: keyof typeof ui) {
  return function t(key: keyof typeof ui[typeof defaultLang]) {
    return ui[lang][key] || ui[defaultLang][key];
  }
}
