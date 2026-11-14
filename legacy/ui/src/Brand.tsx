// The pooltune logo, matching the public site: three bars plus the lowercase wordmark.
// Sized in em so it scales with the surrounding text.
export default function Wordmark({className=''}:{className?:string}){
 return <span className={`wordmark ${className}`.trim()}><span className="wordmark__mark" aria-hidden="true"><i/><i/><i/></span><span className="wordmark__word">pooltune</span></span>;
}
