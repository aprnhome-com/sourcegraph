var React = require("react");
var client = require("../client");
var moment = require("moment");

var RepoBuildIndicator = React.createClass({

	propTypes: {
		// SuccessReload will cause the page to reload when the build becomes
		// successful. The option will be enabled if the prop is set, no matter
		// its value.
		SuccessReload: React.PropTypes.string,

		// RepoURI represents the URI of the repository that we are checking
		// build data for.
		RepoURI: React.PropTypes.string,

		// Rev sets the revision for which we are checking build information.
		Rev: React.PropTypes.string,

		// Label will cause the button to display a label on the left of the icon
		// only if set to "yes".
		Label: React.PropTypes.string,

		// Buildable is whether or not the RepoBuildIndicator will let the
		// user trigger a build if a build does not exist.
		Buildable: React.PropTypes.bool,
	},

	getDefaultProps() {
		return {
			tooltipPosition: "top",
			Buildable: false,
		};
	},

	getInitialState() {
		return {
			LastBuild: this.props.LastBuild,
			status: this._getBuildStatus(this.props.LastBuild),
		};
	},

	componentDidMount() {
		if (this.state.status === this.BuildStatus.UNKNOWN) {
			this.checkBuildStatus();
		}
	},

	componentWillUnmount() {
		clearInterval(this.interval);
	},

	// BuildStatus indicates the current status of the indicator.
	BuildStatus: {
		FAILURE: "FAILURE",
		BUILT: "BUILT",
		STARTED: "STARTED",
		QUEUED: "QUEUED",
		NA: "NOT_AVAILABLE",
		ERROR: "ERROR",
		UNKNOWN: "UNKNOWN",
	},

	// getBuildStatus returns the status appropriate for the given build data.
	_getBuildStatus(buildData) {
		if (typeof buildData === "undefined") {
			return this.BuildStatus.UNKNOWN;
		}
		if (Array.isArray(buildData) && buildData.length === 0 || buildData === null) {
			return this.BuildStatus.NA;
		}
		if (buildData.Failure) {
			return this.BuildStatus.FAILURE;
		}
		if (buildData.Success) {
			return this.BuildStatus.BUILT;
		}
		if (buildData.StartedAt && !buildData.EndedAt) {
			return this.BuildStatus.STARTED;
		}
		return this.BuildStatus.QUEUED;
	},

	// PollSpeeds holds the intervals at which to poll for updates (ms).
	// Keys that are not present will cause no polling.
	PollSpeeds: {
		STARTED: 5000,
		QUEUED: 10000,
	},

	_updatePoller() {
		clearInterval(this.interval);
		var freq = this.PollSpeeds[this.state.status] || 0;
		if (freq) {
			this.interval = setInterval(this.checkBuildStatus, freq);
		}
	},

	// _updateBuild updates the component's state based on new LastBuild data.
	// If the data argument is an Array of builds, the one at index 0 is used.
	_updateBuildData(data) {
		this.setState({LastBuild: data || null, status: this._getBuildStatus(data)});
	},

	// _handleError handles network errors
	_updateBuildDataError(err) {
		this.setState({LastBuild: null, status: this.BuildStatus.ERROR});
	},

	checkBuildStatus() {
		client.builds(this.props.RepoURI, this.props.Rev, this.state.noCache)
			.then(
				data => this._updateBuildData(data && data.Builds ? data.Builds[0] : null),
				this._updateBuildDataError
			);
	},

	triggerBuild(ev) {
		this.setState({noCache: true}); // Otherwise after creating the build, API responses still show the prior state.
		client.createRepoBuild(this.props.RepoURI, this.props.Rev)
			.then(this._updateBuildData, this._updateBuildDataError);
	},

	render() {
		this._updatePoller();
		if (this.state.status === this.BuildStatus.BUILT && this.props.SuccessReload) {
			location.reload();
		}

		var txt, icon, at, cls, label;
		switch (this.state.status) {
		case this.BuildStatus.ERROR:
			return <span className="build-indicator text-danger">Error getting build data.</span>;

		case this.BuildStatus.UNKNOWN:
		case this.BuildStatus.NA:
			if (this.props.Buildable) {
				return (
					<a key="indicator" data-tooltip={this.props.tooltipPosition} title="Click to build." onClick={this.triggerBuild} className={"build-indicator btn " + this.props.btnSize + " btn-warning"}>
						{this.props.label === "yes" ? <span>Not built </span> : null}<i className="fa fa-exclamation-triangle"></i>
					</a>
				);
			}
			return (
				<a key="indicator" data-tooltip={this.props.tooltipPosition} title="Not yet built." className={"build-indicator btn " + this.props.btnSize + " btn-warning"}>
					{this.props.label === "yes" ? <span>Not built </span> : null}<i className="fa fa-exclamation-triangle"></i>
				</a>
			);

		case this.BuildStatus.FAILURE:
			label = "Build failed";
			txt = "build failed";
			at = this.state.LastBuild.EndedAt;
			cls = "danger";
			icon = "fa-exclamation-circle";
			break;

		case this.BuildStatus.BUILT:
			label = "Build OK";
			txt = "built";
			at = this.state.LastBuild.EndedAt;
			cls = "success";
			icon = "fa-check";
			break;

		case this.BuildStatus.STARTED:
			label = "Build started";
			txt = "started";
			at = this.state.LastBuild.StartedAt;
			cls = "info";
			icon = "fa-circle-o-notch fa-spin";
			break;

		case this.BuildStatus.QUEUED:
			label = "Queued";
			txt = "queued";
			at = this.state.LastBuild.CreatedAt;
			cls = "warning";
			icon = "fa-clock-o";
			break;
		}
		return (
			<a key="indicator"
				className={"build-indicator btn " + this.props.btnSize + " btn-"+cls}
				href={"/" + this.props.RepoURI + "/.builds/" + this.state.LastBuild.CommitID + "/" + this.state.LastBuild.Attempt}
				data-tooltip={this.props.tooltipPosition}
				title={this.state.LastBuild.CommitID.slice(0, 6) + " " + txt + " " + moment(at).fromNow()}>
				{this.props.Label === "yes" ? label+" " : ""}<i className={"fa "+icon}></i>
			</a>
		);
	},
});

module.exports = RepoBuildIndicator;
