import {useLayoutEffect,type RefObject} from 'react';
import {gsap} from 'gsap';

// Fades in [data-animate] children once when a view mounts. Polling re-renders do not replay it.
export function useEntrance(ref:RefObject<HTMLElement|null>,key:string){
 useLayoutEffect(()=>{
  const root=ref.current;if(!root)return;
  const mm=gsap.matchMedia();
  mm.add('(prefers-reduced-motion: no-preference)',()=>{gsap.from(root.querySelectorAll('[data-animate]'),{opacity:0,y:10,duration:.35,ease:'power2.out',stagger:.03,clearProps:'opacity,transform'})});
  return()=>mm.revert();
 },[key]);
}
