const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { webcrypto } = require('node:crypto');
const source = fs.readFileSync(path.join(__dirname, '../../wp-panel-optimizer/assets/maintenance.js'), 'utf8');

async function scenario(failure, sameID) {
    class Element {
        constructor() { this.value = ''; this.children = []; this.events = {}; this.classList = {toggle(){}}; this.parentElement = {dataset:{}}; }
        addEventListener(name, fn) { this.events[name] = fn; }
        appendChild(el) { this.children.push(el); }
        replaceChildren() { this.children = []; }
        focus() {}
    }
    const elements = new Map();
    const element = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
    const requests = [];
    const context = {
        window: {WPPMaintenance:{url:'/mock',nonce:'fixture',text:{unlock:'unlock',warning:'warning',verification_frozen:'Paused for 10 minutes'}}},
        document: {getElementById:element,querySelector:()=>element('bar'),querySelectorAll:()=>[],createElement:()=>new Element()},
        crypto:webcrypto, URLSearchParams, Date, setInterval(){},
        fetch:async (_, options)=>{
            const body=Object.fromEntries(options.body);
            if (body.operation==='unlock') {
                requests.push(body.request_id);
                if (failure==='network') throw new Error('network interrupted');
                return {json:async()=>({success:false,data:{message:failure}})};
            }
            return {json:async()=>({success:true,data:{state:'locked',enabled:true,window_id:'',revision:0,server_time:Math.floor(Date.now()/1000)}})};
        }
    };
    vm.runInNewContext(source,context);
    await new Promise(resolve=>setImmediate(resolve));
    for (let i=0;i<2;i++) {
        element('wpp-maintenance-password').value='test-only-password';
        await element('wpp-maintenance-actions').children[0].events.click();
        assert.equal(element('wpp-maintenance-password').value,'');
        if (failure==='verification_frozen') assert.equal(element('wpp-maintenance-message').textContent,'Paused for 10 minutes');
    }
    assert.equal(requests.length,2);
    assert.equal(requests[0]===requests[1],sameID,failure);
}
(async()=>{
    await scenario('verification_failed',false);
    await scenario('verification_frozen',false);
    await scenario('password_required',false);
    await scenario('state_unknown',true);
    await scenario('network',true);
    console.log('maintenance request retry checks passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
