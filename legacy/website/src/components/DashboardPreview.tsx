import {useEffect,useRef} from 'react';
import recording from '../../media/dashboard-preview.mp4';
import poster from '../../media/dashboard-poster.png';

export default function DashboardPreview() {
 const ref=useRef<HTMLVideoElement>(null);
 useEffect(()=>{
  const video=ref.current;
  if(!video)return;
  const motion=window.matchMedia('(prefers-reduced-motion: reduce)');
  let visible=false,pausedByViewer=false,automaticPause=false;
  const pause=()=>{if(!video.paused){automaticPause=true;video.pause()}};
  const onPause=()=>{if(automaticPause)automaticPause=false;else pausedByViewer=true};
  const onPlay=()=>{pausedByViewer=false};
  const sync=()=>{
   if(visible&&!motion.matches&&!document.hidden){if(!pausedByViewer)void video.play().catch(()=>{});}
   else pause();
  };
  const observer=new IntersectionObserver(([entry])=>{visible=entry.isIntersecting;sync()},{threshold:.2});
  video.addEventListener('pause',onPause);
  video.addEventListener('play',onPlay);
  observer.observe(video);
  motion.addEventListener('change',sync);
  document.addEventListener('visibilitychange',sync);
  return()=>{observer.disconnect();video.removeEventListener('pause',onPause);video.removeEventListener('play',onPlay);motion.removeEventListener('change',sync);document.removeEventListener('visibilitychange',sync)};
 },[]);
 return <video ref={ref} src={recording} poster={poster} width="1200" height="840" controls loop muted playsInline preload="metadata" aria-label="Pooltune dashboard recording with simulated traffic"/>;
}
