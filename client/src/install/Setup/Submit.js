import React from 'react';
import { connect } from 'react-redux';
import PropTypes from 'prop-types';
import { reduxForm, formValueSelector } from 'redux-form';
import { Trans, withNamespaces } from 'react-i18next';
import flow from 'lodash/flow';

import Controls from './Controls';

let Submit = props => (
    <div className="setup__step">
        <div className="setup__group">
            <h1 className="setup__title">
                <Trans>install_submit_title</Trans>
            </h1>
            <p className="setup__desc">
                <Trans>install_submit_desc</Trans>
            </p>
        </div>
        <form onSubmit={props.handleSubmit}>
            <Controls
                submitting={props.submitting}
                pristine={props.pristine}
                address={`http://${props.interfaceIp}`}
            />
        </form>
    </div>
);

Submit.propTypes = {
    interfaceIp: PropTypes.string.isRequired,
    interfacePort: PropTypes.number.isRequired,
    handleSubmit: PropTypes.func.isRequired,
    pristine: PropTypes.bool.isRequired,
    submitting: PropTypes.bool.isRequired,
};

const selector = formValueSelector('install');

Submit = connect((state) => {
    const interfaceIp = selector(state, 'web.ip');
    const interfacePort = selector(state, 'web.port');

    return {
        interfaceIp,
        interfacePort,
    };
})(Submit);


export default flow([
    withNamespaces(),
    reduxForm({
        form: 'install',
        destroyOnUnmount: false,
        forceUnregisterOnUnmount: true,
    }),
])(Submit);
