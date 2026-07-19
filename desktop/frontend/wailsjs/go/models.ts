export namespace main {
	
	export class NativeInputStatus {
	    available: boolean;
	    state: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new NativeInputStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.state = source["state"];
	        this.detail = source["detail"];
	    }
	}

}

